package bot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/dyudin0821/bestregards/internal/admin"
)

const (
	secretHeader   = "X-Telegram-Bot-Api-Secret-Token"
	maxUpdateBytes = 1 << 20
	handleTimeout  = 8 * time.Second
)

// Messenger is what the webhook needs to talk back.
type Messenger interface {
	SendMessage(ctx context.Context, chatID int64, text, appURL string) error
	SendPanel(ctx context.Context, chatID int64, p Panel) error
	EditPanel(ctx context.Context, chatID int64, messageID int, p Panel) error
	AnswerCallback(ctx context.Context, queryID, text string) error
}

// Handler is the webhook endpoint of the bot.
type Handler struct {
	Secret   string
	Telegram Messenger
	Admins   map[int64]bool
	Admin    *admin.Service // the panel behind /admin
	AppURL   string         // Mini App address for the /start button, may be empty
	Log      *slog.Logger
}

// ServeHTTP implements http.Handler. Updates are never worth a redelivery, so the answer is 200
// even when something went wrong; the log has the details.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(secretHeader)), []byte(h.Secret)) != 1 || h.Secret == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var u models.Update
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpdateBytes)).Decode(&u); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), handleTimeout)
	defer cancel()

	switch {
	case u.CallbackQuery != nil:
		h.callback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.Chat.Type == models.ChatTypePrivate && u.Message.From != nil:
		h.command(ctx, u.Message)
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) command(ctx context.Context, m *models.Message) {
	cmd, arg, ok := parseCommand(m.Text)
	if !ok {
		return
	}
	switch cmd {
	case "start":
		h.send(ctx, m.Chat.ID, "Переводчик с корпоративного языка и обратно. Скажи как есть — отправь как надо.", h.AppURL)
	case "admin":
		if h.Admins[m.From.ID] {
			h.admin(ctx, m.Chat.ID, arg)
		}
	}
}

func (h *Handler) admin(ctx context.Context, chat int64, arg string) {
	sub, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch sub {
	case "":
		h.sendMenu(ctx, chat)
	case "user":
		h.sendUserCard(ctx, chat, rest)
	default:
		h.say(ctx, chat, "Команды: /admin (панель), /admin user <id>")
	}
}

func (h *Handler) say(ctx context.Context, chat int64, text string) { h.send(ctx, chat, text, "") }

func (h *Handler) send(ctx context.Context, chat int64, text, appURL string) {
	if err := h.Telegram.SendMessage(ctx, chat, text, appURL); err != nil {
		h.Log.WarnContext(ctx, "send message", "chat", chat, "err", err)
	}
}

// parseCommand splits "/cmd@botname args" into its command and arguments.
func parseCommand(text string) (cmd, arg string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, arg, _ := strings.Cut(text[1:], " ")
	cmd, _, _ = strings.Cut(head, "@")
	return strings.ToLower(cmd), strings.TrimSpace(arg), cmd != ""
}
