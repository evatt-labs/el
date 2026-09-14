package resource

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName identifies this package's telemetry.
const instrumentationName = "github.com/evatt-labs/kraai/internal/resource"

// Instrument returns a decorator suitable for WithDecorator, wrapping every
// verb in a span and a duration histogram (D17).
//
// Applied at registration rather than at each call site, so a resource type
// cannot be added without instrumentation by forgetting a wrapper — the
// explicit ask behind D17 was to time calls across the whole stack and find
// hot spots, which only holds if coverage is automatic.
//
// Passing nil for either provider uses the globals, which is what a real run
// does; tests pass an SDK provider with an in-memory exporter.
func Instrument(tp trace.TracerProvider, mp metric.MeterProvider) func(Registration) Resource {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	tracer := tp.Tracer(instrumentationName)
	meter := mp.Meter(instrumentationName)

	// One histogram for every verb of every type, distinguished by
	// attributes rather than by instrument. A metrics backend can then slice
	// by provider, type or verb without kraai deciding in advance which of
	// those someone will want to group by.
	duration, err := meter.Int64Histogram(
		"kraai.resource.duration",
		metric.WithDescription("Duration of a resource verb call."),
		metric.WithUnit("ms"),
	)
	if err != nil {
		// A metrics pipeline that will not build an instrument must not stop
		// a deployment. Tracing and the operation itself are unaffected.
		duration = nil
	}

	return func(reg Registration) Resource {
		return &instrumented{
			inner:    reg.Resource,
			tracer:   tracer,
			duration: duration,
			attrs: []attribute.KeyValue{
				attribute.String("kraai.provider", reg.Provider),
				attribute.String("kraai.resource_type", reg.Type),
				attribute.String("kraai.capability", reg.Capability),
				attribute.String("kraai.phase", reg.Phase.String()),
			},
		}
	}
}

// instrumented wraps a Resource with a span and a timing per verb.
type instrumented struct {
	inner    Resource
	tracer   trace.Tracer
	duration metric.Int64Histogram
	attrs    []attribute.KeyValue
}

// observe runs one verb inside a span, records its duration, and marks the
// span's status from the result.
//
// The resource name is a span attribute but never a metric one: names are
// per-environment and unbounded, so using them as a metric dimension would
// produce a new time series per ephemeral environment and eventually
// overwhelm whatever is storing them. A trace can carry it; a histogram
// cannot.
func (i *instrumented) observe(ctx context.Context, verb, name string, fn func(context.Context) error) error {
	attrs := append(append([]attribute.KeyValue{}, i.attrs...), attribute.String("kraai.verb", verb))

	ctx, span := i.tracer.Start(ctx, "resource."+verb,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(attrs, attribute.String("kraai.resource_name", name))...))
	defer span.End()

	start := time.Now()
	err := fn(ctx)
	elapsed := time.Since(start)

	if i.duration != nil {
		i.duration.Record(ctx, elapsed.Milliseconds(),
			metric.WithAttributes(append(attrs, attribute.Bool("kraai.error", err != nil))...))
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "")
	}
	return err
}

func (i *instrumented) Get(ctx context.Context, ref Ref) (*State, error) {
	var state *State
	err := i.observe(ctx, "get", ref.Name, func(ctx context.Context) error {
		var err error
		state, err = i.inner.Get(ctx, ref)
		return err
	})
	return state, err
}

func (i *instrumented) Create(ctx context.Context, spec Spec) (*State, error) {
	var state *State
	err := i.observe(ctx, "create", spec.Binding, func(ctx context.Context) error {
		var err error
		state, err = i.inner.Create(ctx, spec)
		return err
	})
	return state, err
}

func (i *instrumented) Update(ctx context.Context, ref Ref, spec Spec) (*State, error) {
	var state *State
	err := i.observe(ctx, "update", ref.Name, func(ctx context.Context) error {
		var err error
		state, err = i.inner.Update(ctx, ref, spec)
		return err
	})
	return state, err
}

func (i *instrumented) Delete(ctx context.Context, ref Ref) error {
	return i.observe(ctx, "delete", ref.Name, func(ctx context.Context) error {
		return i.inner.Delete(ctx, ref)
	})
}
