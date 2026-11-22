package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/dyudin0821/bestregards/internal/admin"
)

// Callback data of the admin panel; Telegram limits it to 64 bytes.
const (
	cbMenu         = "a:menu"
	cbSummary      = "a:sum"
	cbBudget       = "a:bud"
	cbFind         = "a:usr"
	cbAskStop      = "a:stop"
	cbResume       = "st:0"
	cbDoStop       = "st:1"
	cbAskGuestsOff = "a:goff"
	cbGuestsOn     = "go:0"
	cbGuestsOff    = "go:1"
	cbUserPre      = "u:" // + user id
)

var navMenu = []Button{{"← Меню", cbMenu}}

func nav(refresh string) [][]Button {
	return [][]Button{{{"Обновить", refresh}, {"← Меню", cbMenu}}}
}

// callback handles a button press. Every press re-checks that the sender is an admin: a menu
// forwarded to someone else must not work for them.
func (h *Handler) callback(ctx context.Context, q *models.CallbackQuery) {
	if !h.Admins[q.From.ID] {
		return
	}
	msg := q.Message.Message
	if msg == nil {
		h.answer(ctx, q.ID, "Сообщение устарело. Откройте /admin заново.")
		return
	}
	panel := h.route(ctx, q.From.ID, q.Data)
	if err := h.Telegram.EditPanel(ctx, msg.Chat.ID, msg.ID, panel); err != nil {
		h.Log.WarnContext(ctx, "edit panel", "err", err)
	}
	h.answer(ctx, q.ID, "")
}

func (h *Handler) answer(ctx context.Context, queryID, text string) {
	if err := h.Telegram.AnswerCallback(ctx, queryID, text); err != nil {
		h.Log.WarnContext(ctx, "answer callback", "err", err)
	}
}

// route turns a button's data into the screen to show.
func (h *Handler) route(ctx context.Context, adminID int64, data string) Panel {
	var (
		p   Panel
		err error
	)
	switch {
	case data == cbSummary:
		p, err = h.summaryPanel(ctx)
	case data == cbBudget:
		p, err = h.budgetPanel(ctx)
	case data == cbAskStop:
		p = Panel{
			Text: "Остановить AI-переводы для всех?\n\nГотовые примеры продолжат работать. Включить обратно можно из меню.",
			Rows: [][]Button{{{"Да, остановить", cbDoStop}, {"Отмена", cbMenu}}},
		}
	case data == cbDoStop || data == cbResume:
		p = h.switchFree(ctx, adminID, data == cbDoStop)
	case data == cbAskGuestsOff:
		p = Panel{
			Text: "Перестать выдавать гостевые аккаунты?\n\nУже выданные гостевые сессии продолжат работать. Включить обратно можно из меню.",
			Rows: [][]Button{{{"Да, остановить гостей", cbGuestsOff}, {"Отмена", cbMenu}}},
		}
	case data == cbGuestsOff || data == cbGuestsOn:
		p = h.switchGuests(ctx, adminID, data == cbGuestsOff)
	case data == cbFind:
		p = Panel{Text: "Чтобы найти пользователя, отправьте боту:\n/admin user <id>\n\nУ гостей сайта id отрицательный.", Rows: [][]Button{navMenu}}
	case strings.HasPrefix(data, cbUserPre):
		id, perr := strconv.ParseInt(strings.TrimPrefix(data, cbUserPre), 10, 64)
		if perr != nil {
			return h.menuPanel(ctx)
		}
		p, err = h.userPanel(ctx, id)
	default:
		p = h.menuPanel(ctx)
	}
	if err != nil {
		h.Log.ErrorContext(ctx, "admin panel", "data", logSafe(data), "err", err)
		return Panel{Text: "Не получилось загрузить данные. Попробуйте ещё раз.", Rows: nav(data)}
	}
	return p
}

func (h *Handler) menuPanel(ctx context.Context) Panel {
	text := "Панель администратора bestregards"
	toggle := Button{"Остановить AI-переводы", cbAskStop}
	switch stopped, err := h.Admin.FreeStopped(ctx); {
	case err != nil:
		h.Log.ErrorContext(ctx, "read free switch", "err", err)
		text += "\n\nСостояние переключателя прочитать не удалось: не доверяйте кнопке ниже, пока база не ответит."
	case stopped:
		text += "\n\nAI-переводы ОСТАНОВЛЕНЫ."
		toggle = Button{"Включить AI-переводы", cbResume}
	}
	guests := Button{"Остановить гостей", cbAskGuestsOff}
	switch off, err := h.Admin.GuestsOff(ctx); {
	case err != nil:
		h.Log.ErrorContext(ctx, "read guests switch", "err", err)
		text += "\n\nСостояние гостей прочитать не удалось: не доверяйте кнопке ниже, пока база не ответит."
	case off:
		text += "\n\nГостевые аккаунты ОСТАНОВЛЕНЫ."
		guests = Button{"Впустить гостей", cbGuestsOn}
	}
	return Panel{Text: text, Rows: [][]Button{
		{{"Сводка за сегодня", cbSummary}, {"Расход на AI", cbBudget}},
		{{"Пользователь по ID", cbFind}},
		{toggle},
		{guests},
	}}
}

func (h *Handler) sendMenu(ctx context.Context, chat int64) {
	if err := h.Telegram.SendPanel(ctx, chat, h.menuPanel(ctx)); err != nil {
		h.Log.WarnContext(ctx, "send menu", "err", err)
	}
}

func (h *Handler) summaryPanel(ctx context.Context) (Panel, error) {
	s, err := h.Admin.Summary(ctx)
	if err != nil {
		return Panel{}, err
	}
	title := fmt.Sprintf("Сегодня, с 00:00 (%s)", h.Admin.Location())
	return Panel{Text: summaryText(title, s), Rows: nav(cbSummary)}, nil
}

func summaryText(title string, s admin.Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", title)
	fmt.Fprintf(&b, "Переводов: %d (готовых %d, AI %d)\n", s.Done(), s.Pair, s.AI)
	fmt.Fprintf(&b, "Скопировано: %d (%.0f%%)\n", s.Copied, s.CopiedPercent())
	fmt.Fprintf(&b, "Людей: %d\n", s.People)
	fmt.Fprintf(&b, "Упёрлись: в квоту %d, в потолок %d\n\n", s.QuotaStops, s.BudgetStops)
	fmt.Fprintf(&b, "Расход на AI: %s", dollars(s.SpentMicro))
	return b.String()
}

func dollars(micro int64) string {
	return fmt.Sprintf("$%.2f", float64(micro)/1e6)
}

func (h *Handler) budgetPanel(ctx context.Context) (Panel, error) {
	b, err := h.Admin.Budget(ctx)
	if err != nil {
		return Panel{}, err
	}
	state := "AI-переводы работают"
	switch {
	case b.FreeStopped:
		state = "AI-переводы ОСТАНОВЛЕНЫ вручную"
	case b.Exhausted():
		state = "AI-переводы остановлены у всех, пока лимит часа не вырастет"
	}
	text := fmt.Sprintf("Расход на AI\n\nпотрачено сегодня %s\nразрешено к этому часу %s\nиз них гости %s из %s\nпотолок на сутки %s\n\nСейчас: %s.\nСутки обновятся в %s.",
		dollars(b.SpentMicro), dollars(b.AllowedMicro), dollars(b.GuestSpentMicro), dollars(b.GuestAllowedMicro), dollars(b.CeilingMicro), state, b.ResetsAt.In(h.Admin.Location()).Format("15:04"))
	return Panel{Text: text, Rows: nav(cbBudget)}, nil
}

func (h *Handler) userPanel(ctx context.Context, id int64) (Panel, error) {
	c, err := h.Admin.User(ctx, id)
	if errors.Is(err, admin.ErrNotFound) {
		return Panel{Text: fmt.Sprintf("Пользователь %d не найден.", id), Rows: [][]Button{navMenu}}, nil
	}
	if err != nil {
		return Panel{}, err
	}
	loc := h.Admin.Location()
	campaign := c.User.Source
	if campaign == "" {
		campaign = "—"
	}
	title := "Пользователь"
	if c.User.Guest() {
		title = "Гость сайта"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d\n\n", title, c.User.ID)
	fmt.Fprintf(&b, "Первый запуск: %s\n", c.User.CreatedAt.In(loc).Format("02.01.2006 15:04"))
	fmt.Fprintf(&b, "Последний визит: %s\n", c.User.LastSeenAt.In(loc).Format("02.01.2006 15:04"))
	fmt.Fprintf(&b, "Кампания: %s\n", campaign)
	if c.User.Banned {
		b.WriteString("Заблокирован: да\n")
	}
	if c.Unlimited {
		fmt.Fprintf(&b, "\nПереводов сегодня: %d (администратор, без лимита)", c.FreeUsed)
	} else {
		fmt.Fprintf(&b, "\nПереводов сегодня: %d из %d", c.FreeUsed, c.FreeLimit)
	}
	return Panel{Text: b.String(), Rows: nav(cbUserPre + strconv.FormatInt(id, 10))}, nil
}

// logSafe strips line breaks from a value that came from outside so it cannot forge log lines.
func logSafe(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}

// audit records who did what in the log, where the operator's log pipeline keeps it. Every
// action that changes availability goes through it.
func (h *Handler) audit(ctx context.Context, adminID int64, action string) {
	h.Log.InfoContext(ctx, "admin action", "audit", true, "admin", logSafe(strconv.FormatInt(adminID, 10)), "action", logSafe(action))
}

// switchFree turns AI translations off or on and shows the menu.
func (h *Handler) switchFree(ctx context.Context, adminID int64, stop bool) Panel {
	action := "free_resume"
	if stop {
		action = "free_stop"
	}
	h.audit(ctx, adminID, action)
	if err := h.Admin.SetFreeStopped(ctx, stop); err != nil {
		h.Log.ErrorContext(ctx, "switch free", "stop", stop, "err", err)
		return Panel{Text: "Не получилось переключить. Ничего не изменено.", Rows: [][]Button{navMenu}}
	}
	return h.menuPanel(ctx)
}

// switchGuests stops or resumes new guest accounts and shows the menu.
func (h *Handler) switchGuests(ctx context.Context, adminID int64, off bool) Panel {
	action := "guests_on"
	if off {
		action = "guests_off"
	}
	h.audit(ctx, adminID, action)
	if err := h.Admin.SetGuestsOff(ctx, off); err != nil {
		h.Log.ErrorContext(ctx, "switch guests", "off", off, "err", err)
		return Panel{Text: "Не получилось переключить. Ничего не изменено.", Rows: [][]Button{navMenu}}
	}
	return h.menuPanel(ctx)
}

// sendUserCard answers `/admin user <id>`.
func (h *Handler) sendUserCard(ctx context.Context, chat int64, arg string) {
	id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil || id == 0 {
		h.say(ctx, chat, "Формат: /admin user <id>")
		return
	}
	p, err := h.userPanel(ctx, id)
	if err != nil {
		h.Log.ErrorContext(ctx, "user card", "id", id, "err", err)
		h.say(ctx, chat, "Не получилось загрузить пользователя.")
		return
	}
	if err := h.Telegram.SendPanel(ctx, chat, p); err != nil {
		h.Log.WarnContext(ctx, "send user card", "err", err)
	}
}
