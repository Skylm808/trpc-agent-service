package metrics

import (
	"context"
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
