package bot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	token  = "123456:TEST"
	secret = "webhook-secret-webhook-secret-1234"
)

// fakeAPI plays the Telegram Bot API: it records every call.
type fakeAPI struct {
	mu    sync.Mutex
	calls []call
	srv   *httptest.Server
}

type call struct {
	Method string
	Body   map[string]any
}

// formBody decodes a Bot API request. The client sends multipart/form-data: scalars as text and
// structured values (arrays, objects, booleans, numbers) as JSON text.
func formBody(r *http.Request) map[string]any {
	body := map[string]any{}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return body
	}
	for k, v := range r.MultipartForm.Value {
		var parsed any
		if err := json.Unmarshal([]byte(v[0]), &parsed); err == nil {
			body[k] = parsed
		} else {
			body[k] = v[0]
		}
	}
	return body
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !strings.HasPrefix(r.URL.Path, "/bot"+token+"/") {
			http.Error(w, "wrong token path", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, call{method, formBody(r)})
		f.mu.Unlock()

		result := any(true)
		switch method {
		case "sendMessage", "editMessageText":
			result = map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": 1, "type": "private"}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) called(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	api *fakeAPI
	h   *Handler
}

func setup(t *testing.T) env {
	t.Helper()
	api := newFakeAPI(t)
	client, err := NewClient(token, api.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return env{api: api, h: &Handler{
		Secret: secret, Telegram: client, Admins: map[int64]bool{900: true},
		AppURL: "https://app.example/app", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
}

func post(h http.Handler, secretValue, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/bot/webhook", strings.NewReader(body))
	if secretValue != "" {
		req.Header.Set(secretHeader, secretValue)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func update(m map[string]any) string {
	m["update_id"] = 1
	b, _ := json.Marshal(m)
	return string(b)
}

func message(from int64, text string) map[string]any {
	return map[string]any{"message": map[string]any{
		"message_id": 1, "date": 1, "text": text,
		"from": map[string]any{"id": from, "is_bot": false, "first_name": "A"},
		"chat": map[string]any{"id": from, "type": "private"},
	}}
}

func TestWebhookRequiresTheSecret(t *testing.T) {
	e := setup(t)
	body := update(message(42, "/start"))
	for name, s := range map[string]string{"missing": "", "wrong": "wrong-secret", "prefix": secret[:10]} {
		if rec := post(e.h, s, body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, rec.Code)
		}
	}
	if len(e.api.calls) != 0 {
		t.Fatalf("an unauthenticated update must do nothing: %+v", e.api.calls)
	}
	if rec := post(e.h, secret, "{not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("garbage body: status %d, want 400", rec.Code)
	}
	empty := &Handler{Telegram: e.h.Telegram, Log: e.h.Log}
	if rec := post(empty, "", body); rec.Code != http.StatusForbidden {
		t.Errorf("an unset secret must never match an empty header: status %d", rec.Code)
	}
}

func TestStartOpensTheMiniAppForEveryone(t *testing.T) {
	e := setup(t)
	if rec := post(e.h, secret, update(message(42, "/start"))); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	sent := e.api.called("sendMessage")
	if len(sent) != 1 || sent[0].Body["chat_id"] != float64(42) {
		t.Fatalf("start reply: %+v", e.api.calls)
	}
	if got := buttonsURL(sent[0].Body); got != "https://app.example/app" {
		t.Fatalf("the button must open the Mini App, got %q", got)
	}
}

func buttonsURL(body map[string]any) string {
	markup, _ := body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		for _, b := range row.([]any) {
			if app, ok := b.(map[string]any)["web_app"].(map[string]any); ok {
				return app["url"].(string)
			}
		}
	}
	return ""
}

func TestOtherMessagesAreIgnored(t *testing.T) {
	e := setup(t)
	post(e.h, secret, update(message(42, "привет")))
	post(e.h, secret, update(message(42, "/balance")))
	group := message(42, "/start")
	group["message"].(map[string]any)["chat"] = map[string]any{"id": -5, "type": "group"}
	post(e.h, secret, update(group))
	if len(e.api.calls) != 0 {
		t.Fatalf("only /start in a private chat is answered: %+v", e.api.calls)
	}
}

func TestSetupRegistersTheWebhookWithTheSecretAndLimitedUpdates(t *testing.T) {
	e := setup(t)
	client, _ := NewClient(token, e.api.srv.URL)
	if err := client.Setup(context.Background(), "https://example.com/bot/webhook", secret); err != nil {
		t.Fatal(err)
	}
	w := e.api.called("setWebhook")[0].Body
	if w["url"] != "https://example.com/bot/webhook" || w["secret_token"] != secret {
		t.Fatalf("webhook: %+v", w)
	}
	got := w["allowed_updates"].([]any)
	if len(got) != 2 || got[0] != "message" || got[1] != "callback_query" {
		t.Fatalf("allowed updates: %v", got)
	}
	if len(e.api.called("deleteMyCommands")) != 1 {
		t.Fatal("stale commands must be cleared")
	}
}

func TestParseCommand(t *testing.T) {
	for in, want := range map[string][2]string{
		"/start":                     {"start", ""},
		"/start@WithBestregards_bot": {"start", ""},
		"/ADMIN user  42 ":           {"admin", "user  42"},
		"/admin@bot user 42":         {"admin", "user 42"},
	} {
		cmd, arg, ok := parseCommand(in)
		if !ok || cmd != want[0] || arg != want[1] {
			t.Errorf("%q: %q %q %v", in, cmd, arg, ok)
		}
	}
	for _, in := range []string{"", "hello", "/", "/@bot"} {
		if _, _, ok := parseCommand(in); ok {
			t.Errorf("%q is not a command", in)
		}
	}
}
