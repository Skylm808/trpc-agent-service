package metrics

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestMetricLabelsCannotCarryUserSessionOrMessageIDs(t *testing.T) {
	attributes := (Labels{TenantID: "t", AppID: "a", Channel: "http", Operation: "runner", Status: "ok"}).attributes()
	for _, item := range attributes {
		switch string(item.Key) {
		case "user.id", "session.id", "message.id", "request.id":
			t.Fatalf("high-cardinality metric key %q", item.Key)
		}
	}
}

func TestSpanAttributesHashCallerControlledIdentifiers(t *testing.T) {
	canary := "secret-or-message-canary"
	attributes := (SpanFields{TenantID: "tenant", AppID: "app", Channel: "http", RequestID: canary, TraceID: canary}).attributes()
	for _, item := range attributes {
		if strings.Contains(item.Value.Emit(), canary) {
			t.Fatalf("span attribute %q leaked caller-controlled value", item.Key)
		}
		switch string(item.Key) {
		case "request.id", "correlation.trace_id", "message", "content", "secret":
			t.Fatalf("unsafe span attribute key %q", item.Key)
		}
	}
}
func TestTraceContextRoundTrip(t *testing.T) {
	old := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(old)
	telemetry, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))
	carrier := telemetry.Inject(ctx)
	restored := telemetry.Extract(context.Background(), carrier)
	if got := trace.SpanContextFromContext(restored).TraceID(); got != traceID {
		t.Fatalf("trace ID=%s", got)
	}
}

func TestTraceContextRejectsBaggageAndUnknownFields(t *testing.T) {
	canary := "secret-canary-must-not-propagate"
	safe := safeTraceCarrier(map[string]string{
		"TraceParent": "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01",
		"tracestate":  "vendor=value",
		"baggage":     "authorization=" + canary,
		"message":     canary,
	})
	if len(safe) != 1 || safe["traceparent"] == "" {
		t.Fatalf("safe trace carrier=%v", safe)
	}
	for key, value := range safe {
		if strings.Contains(key, "baggage") || strings.Contains(value, canary) {
			t.Fatalf("unsafe trace field survived: %s", key)
		}
	}
}
