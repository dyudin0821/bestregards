package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/dyudin0821/bestregards/internal/limits"
	"github.com/dyudin0821/bestregards/internal/pairs"
	"github.com/dyudin0821/bestregards/internal/translate"
	"github.com/dyudin0821/bestregards/internal/user"
)

type memBooks struct{ used int }

func (m *memBooks) ReserveQuota(context.Context, int64, time.Time, int) (int, bool, error) {
	m.used++
	return m.used, true, nil
}
func (m *memBooks) RefundQuota(context.Context, int64, time.Time) error      { return nil }
func (m *memBooks) QuotaUsed(context.Context, int64, time.Time) (int, error) { return m.used, nil }
func (m *memBooks) ReserveBudget(context.Context, time.Time, translate.Claim) (bool, error) {
	return true, nil
}
func (m *memBooks) AddBudgetSpend(context.Context, time.Time, int64, bool) error { return nil }

type stubAI struct{ err error }

func (s stubAI) Complete(context.Context, translate.Request) (translate.Completion, error) {
	return translate.Completion{Text: "ok", Usage: translate.Usage{InputTokens: 10, OutputTokens: 5}}, s.err
}

func tracedTranslator(t *testing.T, ai translate.Completer) (*translate.Service, *sdktrace.TracerProvider, *tracetest.SpanRecorder, string) {
	t.Helper()
	catalog, err := pairs.Load()
	if err != nil {
		t.Fatal(err)
	}
	books := &memBooks{}
	tc := translate.Config{
		Pairs: catalog, Quotas: books, Budget: books, AI: ai, Location: time.UTC,
		Prices: limits.Prices{InputPerMtok: 1, OutputPerMtok: 1}, DailyBudgetMicro: 1_000_000,
	}
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	traceTranslate(&tc, tp, "test-model")
	svc, err := translate.New(tc)
	if err != nil {
		t.Fatal(err)
	}
	return svc, tp, spans, catalog.Modes[0].ID
}

func TestTranslationStepsAreSpansUnderTheRequest(t *testing.T) {
	svc, tp, spans, mode := tracedTranslator(t, stubAI{})
	ctx, root := tp.Tracer("t").Start(context.Background(), "POST /api/translate")
	if _, err := svc.Translate(ctx, user.User{ID: 1}, translate.Request{Text: "своё письмо", Mode: mode, Level: 1}); err != nil {
		t.Fatal(err)
	}
	root.End()

	var names []string
	for _, s := range spans.Ended() {
		if s.Name() == "POST /api/translate" {
			continue
		}
		names = append(names, s.Name())
		if s.Parent().SpanID() != root.SpanContext().SpanID() {
			t.Errorf("%s is not a child of the request span", s.Name())
		}
		for _, a := range s.Attributes() {
			if a.Value.String() == "своё письмо" {
				t.Errorf("%s carries the text in %s", s.Name(), a.Key)
			}
		}
	}
	for _, want := range []string{"quota.reserve", "budget.reserve", "ai.complete", "budget.adjust"} {
		if !slices.Contains(names, want) {
			t.Errorf("no %s span in %v", want, names)
		}
	}
}

func TestFailedProviderCallIsAnErrorSpan(t *testing.T) {
	svc, _, spans, mode := tracedTranslator(t, stubAI{err: errors.New("timeout")})
	svc.Translate(context.Background(), user.User{ID: 1}, translate.Request{Text: "своё письмо", Mode: mode, Level: 1})
	for _, s := range spans.Ended() {
		if s.Name() == "ai.complete" {
			if s.Status().Code != codes.Error || len(s.Events()) == 0 {
				t.Errorf("status %v, events %v", s.Status(), s.Events())
			}
			return
		}
	}
	t.Fatal("no ai.complete span")
}
