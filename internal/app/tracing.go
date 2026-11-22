package app

import (
	"context"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/dyudin0821/bestregards/internal/translate"
)

// The translation use case may not import OpenTelemetry (see internal/arch), so its steps are
// traced here, by wrapping the ports it calls. Every span is a child of the request's span and the
// parent of the SQL or HTTP spans of the adapter underneath. No span carries a text or a user id.

const translateTracer = "github.com/dyudin0821/bestregards/internal/translate"

func end(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed")
	}
	span.End()
}

type tracedQuotas struct {
	next   translate.Quotas
	tracer trace.Tracer
}

func (t tracedQuotas) ReserveQuota(ctx context.Context, userID int64, day time.Time, limit int) (used int, ok bool, err error) {
	ctx, span := t.tracer.Start(ctx, "quota.reserve", trace.WithAttributes(attribute.Int("quota.limit", limit)))
	defer func() { end(span, err) }()
	used, ok, err = t.next.ReserveQuota(ctx, userID, day, limit)
	span.SetAttributes(attribute.Bool("quota.granted", ok), attribute.Int("quota.used", used))
	return used, ok, err
}

func (t tracedQuotas) RefundQuota(ctx context.Context, userID int64, day time.Time) (err error) {
	ctx, span := t.tracer.Start(ctx, "quota.refund")
	defer func() { end(span, err) }()
	return t.next.RefundQuota(ctx, userID, day)
}

func (t tracedQuotas) QuotaUsed(ctx context.Context, userID int64, day time.Time) (used int, err error) {
	ctx, span := t.tracer.Start(ctx, "quota.read")
	defer func() { end(span, err) }()
	used, err = t.next.QuotaUsed(ctx, userID, day)
	span.SetAttributes(attribute.Int("quota.used", used))
	return used, err
}

type tracedBudget struct {
	next   translate.Budget
	tracer trace.Tracer
}

func (t tracedBudget) ReserveBudget(ctx context.Context, day time.Time, c translate.Claim) (ok bool, err error) {
	ctx, span := t.tracer.Start(ctx, "budget.reserve", trace.WithAttributes(
		attribute.Int64("budget.reserve_micro", c.Micro), attribute.Int64("budget.cap_micro", c.Cap), attribute.Bool("budget.guest", c.Guest)))
	defer func() { end(span, err) }()
	ok, err = t.next.ReserveBudget(ctx, day, c)
	span.SetAttributes(attribute.Bool("budget.claimed", ok))
	return ok, err
}

func (t tracedBudget) AddBudgetSpend(ctx context.Context, day time.Time, delta int64, guest bool) (err error) {
	ctx, span := t.tracer.Start(ctx, "budget.adjust", trace.WithAttributes(attribute.Int64("budget.delta_micro", delta), attribute.Bool("budget.guest", guest)))
	defer func() { end(span, err) }()
	return t.next.AddBudgetSpend(ctx, day, delta, guest)
}

type tracedSwitch struct {
	next   translate.FreeSwitch
	tracer trace.Tracer
}

func (t tracedSwitch) FreeStopped(ctx context.Context) (stopped bool, err error) {
	ctx, span := t.tracer.Start(ctx, "free_switch.read")
	defer func() { end(span, err) }()
	stopped, err = t.next.FreeStopped(ctx)
	span.SetAttributes(attribute.Bool("free_switch.stopped", stopped))
	return stopped, err
}

type tracedCompleter struct {
	next   translate.Completer
	tracer trace.Tracer
	model  string
}

func (t tracedCompleter) Complete(ctx context.Context, req translate.Request) (c translate.Completion, err error) {
	ctx, span := t.tracer.Start(ctx, "ai.complete", trace.WithAttributes(
		attribute.String("ai.model", t.model),
		attribute.String("translate.mode", req.Mode),
		attribute.Int("translate.level", req.Level),
		attribute.Bool("translate.another_variant", req.Previous != ""),
		attribute.Int("text.runes", utf8.RuneCountInString(req.Text)),
	))
	defer func() { end(span, err) }()
	c, err = t.next.Complete(ctx, req)
	span.SetAttributes(attribute.Int("ai.input_tokens", c.Usage.InputTokens), attribute.Int("ai.output_tokens", c.Usage.OutputTokens))
	return c, err
}

// traceTranslate wraps the ports of tc that are set.
func traceTranslate(tc *translate.Config, tp trace.TracerProvider, model string) {
	tracer := tp.Tracer(translateTracer)
	tc.Quotas = tracedQuotas{tc.Quotas, tracer}
	tc.Budget = tracedBudget{tc.Budget, tracer}
	if tc.Switch != nil {
		tc.Switch = tracedSwitch{tc.Switch, tracer}
	}
	if tc.AI != nil {
		tc.AI = tracedCompleter{tc.AI, tracer, model}
	}
}
