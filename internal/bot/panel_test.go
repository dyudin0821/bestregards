package bot

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dyudin0821/bestregards/internal/admin"
	"github.com/dyudin0821/bestregards/internal/user"
)

// panelReader is a canned read side for the panel.
type panelReader struct {
	admin.Reader
	users     map[int64]user.User
	stopped   bool
	guestsOff bool
}

func (r *panelReader) GuestsOff(context.Context) (bool, error)      { return r.guestsOff, nil }
func (r *panelReader) SetGuestsOff(_ context.Context, v bool) error { r.guestsOff = v; return nil }
func (r *panelReader) GuestBudgetSpent(context.Context, time.Time) (int64, error) {
	return 300_000, nil
}

func (r *panelReader) FreeStopped(context.Context) (bool, error)      { return r.stopped, nil }
func (r *panelReader) SetFreeStopped(_ context.Context, v bool) error { r.stopped = v; return nil }
func (r *panelReader) Activity(context.Context, time.Time, time.Time) (admin.Activity, error) {
	return admin.Activity{Pair: 6, AI: 4, Copied: 5, People: 3, QuotaStops: 2}, nil
}
func (r *panelReader) BudgetSpent(context.Context, time.Time) (int64, error) { return 1_230_000, nil }
func (r *panelReader) GetUser(_ context.Context, id int64) (user.User, error) {
	if u, ok := r.users[id]; ok {
		return u, nil
	}
	return user.User{}, user.ErrNotFound
}
func (r *panelReader) QuotaUsed(context.Context, int64, time.Time) (int, error) { return 2, nil }

func panelSetup(t *testing.T) env {
	t.Helper()
	e, _ := panelSetupWith(t)
	return e
}

func panelSetupWith(t *testing.T) (env, *panelReader) {
	t.Helper()
	e := setup(t)
	when := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	reader := &panelReader{users: map[int64]user.User{
		42: {ID: 42, Source: "tt_launch", CreatedAt: when, LastSeenAt: when},
		-3: {ID: -3, CreatedAt: when, LastSeenAt: when},
	}}
	e.h.Admin = admin.New(reader, admin.Config{CeilingMicro: 24_000_000, Location: time.UTC, Now: func() time.Time { return when }})
	return e, reader
}

func press(from int64, data string) map[string]any {
	return map[string]any{"callback_query": map[string]any{
		"id": "cq-1", "data": data,
		"from":    map[string]any{"id": from, "is_bot": false, "first_name": "A"},
		"message": map[string]any{"message_id": 77, "date": 1, "chat": map[string]any{"id": from, "type": "private"}},
	}}
}

// buttons flattens the callback data of a request's inline keyboard.
func buttons(body map[string]any) []string {
	var out []string
	markup, _ := body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		for _, b := range row.([]any) {
			out = append(out, b.(map[string]any)["callback_data"].(string))
		}
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestAdminCommandOpensTheMenuForAdminsOnly(t *testing.T) {
	e := panelSetup(t)
	post(e.h, secret, update(message(7, "/admin")))
	if len(e.api.calls) != 0 {
		t.Fatalf("a stranger must get nothing: %+v", e.api.calls)
	}
	post(e.h, secret, update(message(900, "/admin")))
	sent := e.api.called("sendMessage")
	if len(sent) != 1 {
		t.Fatalf("menu not sent: %+v", e.api.calls)
	}
	got := buttons(sent[0].Body)
	for _, want := range []string{cbSummary, cbBudget, cbFind, cbAskStop} {
		if !has(got, want) {
			t.Errorf("menu lacks %q: %v", want, got)
		}
	}
	for _, data := range got {
		if len(data) > 64 {
			t.Errorf("callback data %q is over Telegram's 64-byte limit", data)
		}
	}
}

func TestPressesFromNonAdminsDoNothing(t *testing.T) {
	e := panelSetup(t)
	for _, data := range []string{cbSummary, cbBudget, cbDoStop, cbUserPre + "42"} {
		post(e.h, secret, update(press(7, data)))
	}
	if len(e.api.calls) != 0 {
		t.Fatalf("a forwarded menu must not work for a stranger: %+v", e.api.calls)
	}
}

func TestSummaryAndBudgetScreens(t *testing.T) {
	e := panelSetup(t)
	post(e.h, secret, update(press(900, cbSummary)))
	edit := e.api.called("editMessageText")
	if len(edit) != 1 || edit[0].Body["message_id"] != float64(77) || edit[0].Body["chat_id"] != float64(900) {
		t.Fatalf("the menu message must be edited in place: %+v", edit)
	}
	text := edit[0].Body["text"].(string)
	for _, want := range []string{"Переводов: 10 (готовых 6, AI 4)", "Скопировано: 5 (50%)", "Людей: 3", "в квоту 2", "Расход на AI: $1.23"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary lacks %q:\n%s", want, text)
		}
	}
	if len(e.api.called("answerCallbackQuery")) != 1 {
		t.Fatal("the spinner on the button must be stopped")
	}

	post(e.h, secret, update(press(900, cbBudget)))
	text = e.api.called("editMessageText")[1].Body["text"].(string)
	if !strings.Contains(text, "$1.23") || !strings.Contains(text, "$24.00") {
		t.Errorf("budget screen:\n%s", text)
	}
}

func TestUserCardByCommandAndByButton(t *testing.T) {
	e := panelSetup(t)
	post(e.h, secret, update(message(900, "/admin user 42")))
	text := e.api.called("sendMessage")[0].Body["text"].(string)
	// An ad user on the first day has the smaller allowance.
	for _, want := range []string{"Пользователь 42", "Кампания: tt_launch", "Переводов сегодня: 2 из 3"} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}

	post(e.h, secret, update(message(900, "/admin user -3")))
	if m := e.api.called("sendMessage"); !strings.Contains(m[len(m)-1].Body["text"].(string), "Гость сайта -3") {
		t.Errorf("a guest must be recognisable: %+v", m[len(m)-1].Body)
	}
	post(e.h, secret, update(message(900, "/admin user 404")))
	if m := e.api.called("sendMessage"); !strings.Contains(m[len(m)-1].Body["text"].(string), "не найден") {
		t.Errorf("unknown user: %+v", m[len(m)-1].Body)
	}
	for _, bad := range []string{"abc", "0", ""} {
		post(e.h, secret, update(message(900, "/admin user "+bad)))
		if m := e.api.called("sendMessage"); !strings.Contains(m[len(m)-1].Body["text"].(string), "Формат") {
			t.Errorf("bad id %q: %+v", bad, m[len(m)-1].Body)
		}
	}

	post(e.h, secret, update(press(900, cbUserPre+"42")))
	if edit := e.api.called("editMessageText"); len(edit) != 1 || !strings.Contains(edit[0].Body["text"].(string), "Пользователь 42") {
		t.Errorf("button: %+v", edit)
	}
}

// auditLog captures what the handler writes to its logger.
type auditLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *auditLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, string(p))
	return len(p), nil
}

func (l *auditLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "")
}

func withAuditLog(e env) *auditLog {
	l := &auditLog{}
	e.h.Log = slog.New(slog.NewTextHandler(l, nil))
	return l
}

func TestStoppingGuestsNeedsConfirmationAndIsLogged(t *testing.T) {
	e, reader := panelSetupWith(t)
	log := withAuditLog(e)

	post(e.h, secret, update(press(900, cbAskGuestsOff)))
	ask := e.api.called("editMessageText")[0].Body
	if reader.guestsOff || !has(buttons(ask), cbGuestsOff) {
		t.Fatalf("asking must not stop anything: %+v", ask)
	}

	post(e.h, secret, update(press(7, cbGuestsOff))) // a stranger
	if reader.guestsOff || strings.Contains(log.text(), "guests_off") {
		t.Fatal("a stranger must not stop guests, nor leave a trace as an admin")
	}

	post(e.h, secret, update(press(900, cbGuestsOff)))
	menu := e.api.called("editMessageText")[1].Body
	if !reader.guestsOff || !strings.Contains(menu["text"].(string), "Гостевые аккаунты ОСТАНОВЛЕНЫ") {
		t.Fatalf("after stop: off=%v %+v", reader.guestsOff, menu)
	}
	if !has(buttons(menu), cbGuestsOn) || has(buttons(menu), cbAskGuestsOff) {
		t.Fatalf("the menu must now offer to let guests back in: %v", buttons(menu))
	}
	if reader.stopped {
		t.Fatal("stopping guests must not stop AI translations for everyone")
	}
	if out := log.text(); !strings.Contains(out, "action=guests_off") || !strings.Contains(out, "admin=900") {
		t.Fatalf("log:\n%s", out)
	}

	post(e.h, secret, update(press(900, cbGuestsOn)))
	if reader.guestsOff || !strings.Contains(log.text(), "action=guests_on") {
		t.Fatalf("resume: off=%v\n%s", reader.guestsOff, log.text())
	}
}

func TestStoppingAITranslationsNeedsConfirmationAndIsLogged(t *testing.T) {
	e, reader := panelSetupWith(t)
	log := withAuditLog(e)

	post(e.h, secret, update(press(900, cbAskStop)))
	ask := e.api.called("editMessageText")[0].Body
	if reader.stopped || !has(buttons(ask), cbDoStop) || !strings.Contains(ask["text"].(string), "Готовые примеры продолжат") {
		t.Fatalf("asking must not stop anything: %+v", ask)
	}

	post(e.h, secret, update(press(7, cbDoStop))) // a stranger
	if reader.stopped || strings.Contains(log.text(), "free_stop") {
		t.Fatal("a stranger must not stop anything, nor leave a trace as an admin")
	}

	post(e.h, secret, update(press(900, cbDoStop)))
	menu := e.api.called("editMessageText")[1].Body
	if !reader.stopped || !strings.Contains(menu["text"].(string), "ОСТАНОВЛЕНЫ") || !has(buttons(menu), cbResume) || has(buttons(menu), cbAskStop) {
		t.Fatalf("after stop: stopped=%v %+v", reader.stopped, menu)
	}
	if out := log.text(); !strings.Contains(out, "action=free_stop") || !strings.Contains(out, "admin=900") {
		t.Fatalf("log:\n%s", out)
	}

	post(e.h, secret, update(press(900, cbBudget)))
	if text := e.api.called("editMessageText")[2].Body["text"].(string); !strings.Contains(text, "ОСТАНОВЛЕНЫ вручную") {
		t.Errorf("budget screen must say so:\n%s", text)
	}

	post(e.h, secret, update(press(900, cbResume)))
	if reader.stopped || !strings.Contains(log.text(), "action=free_resume") {
		t.Fatalf("resume: stopped=%v\n%s", reader.stopped, log.text())
	}
}
