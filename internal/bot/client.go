// Package bot is the Telegram adapter: a thin client for the Bot API calls the operator panel needs
// and the webhook that turns Telegram updates into calls to the use cases.
package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Client calls the Bot API.
type Client struct{ b *tgbot.Bot }

// NewClient builds a client. apiURL may be empty (the real Bot API); tests point it at a fake.
func NewClient(token, apiURL string) (*Client, error) {
	opts := []tgbot.Option{
		tgbot.WithSkipGetMe(), // no network at construction time
		tgbot.WithHTTPClient(time.Minute, apiClient{http: &http.Client{Timeout: 15 * time.Second}, tracer: otel.Tracer(tracerName)}),
	}
	if apiURL != "" {
		opts = append(opts, tgbot.WithServerURL(strings.TrimRight(apiURL, "/")))
	}
	b, err := tgbot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram client: %w", err)
	}
	return &Client{b: b}, nil
}

// SendMessage sends text to a chat. A non-empty appURL adds a button that opens the Mini App.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text, appURL string) error {
	p := &tgbot.SendMessageParams{ChatID: chatID, Text: text}
	if appURL != "" {
		p.ReplyMarkup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "Открыть bestregards", WebApp: &models.WebAppInfo{URL: appURL}},
		}}}
	}
	_, err := c.b.SendMessage(ctx, p)
	return err
}

// Button is one inline button; Data comes back to the webhook when it is pressed.
type Button struct{ Text, Data string }

// Panel is a message with rows of buttons.
type Panel struct {
	Text string
	Rows [][]Button
}

func (p Panel) markup() *models.InlineKeyboardMarkup {
	kb := make([][]models.InlineKeyboardButton, 0, len(p.Rows))
	for _, row := range p.Rows {
		r := make([]models.InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			r = append(r, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
		kb = append(kb, r)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: kb}
}

// SendPanel sends a new message with buttons.
func (c *Client) SendPanel(ctx context.Context, chatID int64, p Panel) error {
	_, err := c.b.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: chatID, Text: p.Text, ReplyMarkup: p.markup()})
	return err
}

// EditPanel replaces the text and buttons of a message the bot sent earlier. Telegram refuses an
// edit that changes nothing; that is not an error for the caller.
func (c *Client) EditPanel(ctx context.Context, chatID int64, messageID int, p Panel) error {
	_, err := c.b.EditMessageText(ctx, &tgbot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: p.Text, ReplyMarkup: p.markup()})
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

// AnswerCallback stops the spinner on a pressed button, optionally with a short toast.
func (c *Client) AnswerCallback(ctx context.Context, queryID, text string) error {
	_, err := c.b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: queryID, Text: text})
	return err
}

// Setup registers the webhook and clears the command list: the bot has only /start and the
// operator's /admin, neither of which belongs in everyone's command menu. It is run once by
// `bestregards setup-webhook`.
func (c *Client) Setup(ctx context.Context, webhookURL, secret string) error {
	if _, err := c.b.SetWebhook(ctx, &tgbot.SetWebhookParams{
		URL: webhookURL, SecretToken: secret, AllowedUpdates: []string{"message", "callback_query"},
	}); err != nil {
		return fmt.Errorf("set webhook: %w", err)
	}
	if _, err := c.b.DeleteMyCommands(ctx, &tgbot.DeleteMyCommandsParams{}); err != nil {
		return fmt.Errorf("clear commands: %w", err)
	}
	return nil
}

const tracerName = "github.com/dyudin0821/bestregards/internal/bot"

// apiClient sends the Bot API requests. The URL of every request holds the bot token
// (/bot<token>/<method>), so neither its span nor its error may show the URL: the span is named
// after the method, and a transport error keeps only its cause.
type apiClient struct {
	http   *http.Client
	tracer trace.Tracer
}

func (c apiClient) Do(req *http.Request) (*http.Response, error) {
	method := path.Base(req.URL.Path)
	ctx, span := c.tracer.Start(req.Context(), "telegram "+method, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("telegram.method", method), attribute.String("server.address", req.URL.Hostname())))
	defer span.End()

	resp, err := c.http.Do(req.WithContext(ctx)) // #nosec G704 -- the address is the Bot API URL from our own config, never from a request
	if ue := (*url.Error)(nil); errors.As(err, &ue) {
		err = fmt.Errorf("%s telegram %s: %w", ue.Op, method, ue.Err)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "transport")
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, resp.Status)
	}
	return resp, nil
}
