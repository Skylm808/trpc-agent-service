// Package metrics exposes low-cardinality OpenTelemetry metrics and trace propagation.
package metrics

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Labels struct{ TenantID, AppID, Channel, Operation, Status string }

func (labels Labels) attributes() []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String("tenant.id", labels.TenantID), attribute.String("app.id", labels.AppID), attribute.String("channel", labels.Channel), attribute.String("operation", labels.Operation), attribute.String("status", labels.Status)}
}

type SpanFields struct{ TenantID, AppID, Channel, RequestID, TraceID string }

type telemetryContextKey struct{}
type ContextTelemetry struct {
	Telemetry *Telemetry
	Fields    SpanFields
}

func WithTelemetry(ctx context.Context, telemetry *Telemetry, fields SpanFields) context.Context {
	return context.WithValue(ctx, telemetryContextKey{}, ContextTelemetry{Telemetry: telemetry, Fields: fields})
}
func FromContext(ctx context.Context) (ContextTelemetry, bool) {
	value, ok := ctx.Value(telemetryContextKey{}).(ContextTelemetry)
	return value, ok
}

func (fields SpanFields) attributes() []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String("tenant.id", fields.TenantID), attribute.String("app.id", fields.AppID), attribute.String("channel", fields.Channel), attribute.String("request.id", fields.RequestID), attribute.String("correlation.trace_id", fields.TraceID)}
}

type Telemetry struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	requests   metric.Int64Counter
	duration   metric.Float64Histogram
	firstToken metric.Float64Histogram
	tokens     metric.Int64Counter
	cost       metric.Int64Counter
	delivery   metric.Int64Counter
	backlog    metric.Int64Histogram
}

func New(name string) (*Telemetry, error) {
	if name == "" {
		name = "trpc-agent-service"
	}
	meter := otel.Meter(name)
	requests, err := meter.Int64Counter("agent.requests")
	if err != nil {
		return nil, err
	}
	duration, err := meter.Float64Histogram("agent.operation.duration", metric.WithUnit("ms"))
	if err != nil {
		return nil, err
	}
	firstToken, err := meter.Float64Histogram("agent.model.first_token.duration", metric.WithUnit("ms"))
	if err != nil {
		return nil, err
	}
	tokens, err := meter.Int64Counter("agent.tokens")
	if err != nil {
		return nil, err
	}
	cost, err := meter.Int64Counter("agent.cost", metric.WithUnit("us"))
	if err != nil {
		return nil, err
	}
	delivery, err := meter.Int64Counter("agent.im.delivery")
	if err != nil {
		return nil, err
	}
	backlog, err := meter.Int64Histogram("agent.outbox.backlog")
	if err != nil {
		return nil, err
	}
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	return &Telemetry{tracer: otel.Tracer(name), propagator: propagator, requests: requests, duration: duration, firstToken: firstToken, tokens: tokens, cost: cost, delivery: delivery, backlog: backlog}, nil
}

// ModelFirstToken records time to the first model event. The label set is the
// same bounded set used by all other service metrics.
func (telemetry *Telemetry) ModelFirstToken(ctx context.Context, labels Labels, duration time.Duration) {
	if telemetry == nil {
		return
	}
	telemetry.firstToken.Record(ctx, float64(duration.Microseconds())/1000, metric.WithAttributes(labels.attributes()...))
}
func (telemetry *Telemetry) Start(ctx context.Context, name string, fields SpanFields) (context.Context, trace.Span) {
	if telemetry == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return telemetry.tracer.Start(ctx, name, trace.WithAttributes(fields.attributes()...))
}
func (telemetry *Telemetry) Inject(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	if telemetry != nil {
		telemetry.propagator.Inject(ctx, carrier)
	}
	return map[string]string(carrier)
}
func (telemetry *Telemetry) Extract(ctx context.Context, carrier map[string]string) context.Context {
	if telemetry == nil {
		return ctx
	}
	return telemetry.propagator.Extract(ctx, propagation.MapCarrier(carrier))
}
func (telemetry *Telemetry) ExtractHTTP(ctx context.Context, header http.Header) context.Context {
	if telemetry == nil {
		return ctx
	}
	return telemetry.propagator.Extract(ctx, propagation.HeaderCarrier(header))
}
func (telemetry *Telemetry) Request(ctx context.Context, labels Labels, duration time.Duration, tokens, costMicros int64) {
	if telemetry == nil {
		return
	}
	options := metric.WithAttributes(labels.attributes()...)
	telemetry.requests.Add(ctx, 1, options)
	telemetry.duration.Record(ctx, float64(duration.Microseconds())/1000, options)
	if tokens > 0 {
		telemetry.tokens.Add(ctx, tokens, options)
	}
	if costMicros > 0 {
		telemetry.cost.Add(ctx, costMicros, options)
	}
}
func (telemetry *Telemetry) Delivery(ctx context.Context, labels Labels, success bool) {
	if telemetry == nil {
		return
	}
	if success {
		labels.Status = "success"
	} else {
		labels.Status = "failed"
	}
	telemetry.delivery.Add(ctx, 1, metric.WithAttributes(labels.attributes()...))
}
func (telemetry *Telemetry) OutboxBacklog(ctx context.Context, labels Labels, value int64) {
	if telemetry == nil {
		return
	}
	telemetry.backlog.Record(ctx, value, metric.WithAttributes(labels.attributes()...))
}
