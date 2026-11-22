package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAPICallsAreTracedWithoutTheToken(t *testing.T) {
	api := newFakeAPI(t)
	spans := tracetest.NewSpanRecorder()
	c := apiClient{http: api.srv.Client(), tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("t")}

	req := httptest.NewRequest("POST", api.srv.URL+"/bot"+token+"/sendMessage", nil)
	req.RequestURI = ""
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Name() != "telegram sendMessage" {
		t.Fatalf("spans: %v", ended)
	}
	for _, a := range ended[0].Attributes() {
		if strings.Contains(a.Value.String(), token) {
			t.Errorf("attribute %s carries the token", a.Key)
		}
	}
}

func TestTransportErrorsDoNotShowTheToken(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	spans := tracetest.NewSpanRecorder()
	c := apiClient{http: &http.Client{}, tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("t")}

	req, _ := http.NewRequest("POST", dead.URL+"/bot"+token+"/sendMessage", nil)
	resp, err := c.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "sendMessage") {
		t.Fatalf("error %v: want the method without the token", err)
	}
	for _, ev := range spans.Ended()[0].Events() {
		for _, a := range ev.Attributes {
			if strings.Contains(a.Value.String(), token) {
				t.Errorf("span event carries the token: %s", a.Value.String())
			}
		}
	}
}
