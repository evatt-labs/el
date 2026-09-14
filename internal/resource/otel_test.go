package resource

import (
	"context"
	"errors"
	"testing"

	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/mock/gomock"
)

// telemetry wires an in-memory span recorder and metric reader, so the
// acceptance criterion holds with no backend running anywhere.
type telemetry struct {
	spans  *tracetest.SpanRecorder
	reader *metric.ManualReader
	decor  func(Registration) Resource
}

func newTelemetry() *telemetry {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	return &telemetry{spans: spans, reader: reader, decor: Instrument(tp, mp)}
}

// histogram returns the recorded duration histogram, or nil if none exists.
func (tl *telemetry) histogram(t *testing.T) *metricdata.Histogram[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := tl.reader.Collect(t.Context(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "kraai.resource.duration" {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[int64])
			if !ok {
				t.Fatalf("duration metric is %T, want an int64 histogram", m.Data)
			}
			return &h
		}
	}
	return nil
}

// TestInstrumentProducesASpanAndAHistogram is the workstream's acceptance
// criterion: a fake resource called through the decorator produces a span and
// a histogram data point observable through the SDK's in-memory exporter.
func TestInstrumentProducesASpanAndAHistogram(t *testing.T) {
	tl := newTelemetry()
	inner := NewMockResource(gomock.NewController(t))
	inner.EXPECT().
		Get(gomock.Any(), gomock.Any()).
		Return(&State{ID: "db-1"}, nil)

	r := NewRegistry(WithDecorator(tl.decor))
	if err := r.Register(Registration{
		Provider: "cloudflare", Type: "d1_database", Capability: "database",
		Phase: PhaseStorage, Lookup: LookupByAPI, Resource: inner,
	}); err != nil {
		t.Fatal(err)
	}
	entry, _ := r.Lookup("cloudflare/d1_database")

	if _, err := entry.Resource.Get(t.Context(), Ref{
		Provider: "cloudflare", Type: "d1_database", Name: "env-a-api-db",
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	spans := tl.spans.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Name() != "resource.get" {
		t.Fatalf("span name = %q", spans[0].Name())
	}

	attrs := map[string]string{}
	for _, kv := range spans[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value.String()
	}
	for key, want := range map[string]string{
		"kraai.provider":      "cloudflare",
		"kraai.resource_type": "d1_database",
		"kraai.verb":          "get",
		"kraai.phase":         "storage",
		"kraai.resource_name": "env-a-api-db",
	} {
		if attrs[key] != want {
			t.Errorf("span attribute %s = %q, want %q", key, attrs[key], want)
		}
	}

	hist := tl.histogram(t)
	if hist == nil {
		t.Fatal("no duration histogram was recorded")
	}
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 {
		t.Fatalf("histogram data points = %+v, want exactly one observation", hist.DataPoints)
	}
}

// TestInstrumentRecordsFailures: a verb that fails must still be timed, and
// the span must say it failed — an error path that vanishes from telemetry is
// the one you most want to see.
func TestInstrumentRecordsFailures(t *testing.T) {
	tl := newTelemetry()
	inner := NewMockResource(gomock.NewController(t))
	inner.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(errors.New("api refused"))

	wrapped := tl.decor(Registration{
		Provider: "neon", Type: "branch", Capability: "postgres",
		Phase: PhaseDatabase, Lookup: LookupByAttr, Resource: inner,
	})

	if err := wrapped.Delete(t.Context(), Ref{Name: "env-a"}); err == nil {
		t.Fatal("the underlying failure was swallowed by the decorator")
	}

	spans := tl.spans.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Status().Code.String() != "Error" {
		t.Fatalf("span status = %v, want Error", spans[0].Status().Code)
	}
	if len(spans[0].Events()) == 0 {
		t.Fatal("the error was not recorded on the span")
	}
	if tl.histogram(t) == nil {
		t.Fatal("a failing call was not timed")
	}
}

// TestInstrumentDoesNotUseNameAsAMetricDimension: resource names are
// per-environment and unbounded, so using one as a metric attribute produces
// a new time series per ephemeral environment and eventually overwhelms
// whatever stores them. A trace can carry it; a histogram cannot.
func TestInstrumentDoesNotUseNameAsAMetricDimension(t *testing.T) {
	tl := newTelemetry()
	ctrl := gomock.NewController(t)
	inner := NewMockResource(ctrl)
	inner.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, nil).Times(3)

	wrapped := tl.decor(Registration{
		Provider: "cloudflare", Type: "kv_namespace", Capability: "keyvalue",
		Phase: PhaseStorage, Lookup: LookupByAttr, Resource: inner,
	})

	// Three different environments, which is the case that would explode the
	// cardinality.
	for _, name := range []string{"env-a-api-kv", "env-b-api-kv", "env-c-api-kv"} {
		if _, err := wrapped.Get(t.Context(), Ref{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	hist := tl.histogram(t)
	if hist == nil {
		t.Fatal("no histogram recorded")
	}
	if len(hist.DataPoints) != 1 {
		t.Fatalf("got %d series for three environments — the resource name leaked into a metric dimension", len(hist.DataPoints))
	}
	if hist.DataPoints[0].Count != 3 {
		t.Fatalf("series count = %d, want all three observations in one series", hist.DataPoints[0].Count)
	}
}

// Every verb must be instrumented, or the one that is not is invisible
// exactly when it is slow.
func TestEveryVerbIsInstrumented(t *testing.T) {
	tl := newTelemetry()
	ctrl := gomock.NewController(t)
	inner := NewMockResource(ctrl)
	inner.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, nil)
	inner.EXPECT().Create(gomock.Any(), gomock.Any()).Return(&State{}, nil)
	inner.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(&State{}, nil)
	inner.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	wrapped := tl.decor(Registration{
		Provider: "p", Type: "t", Capability: "c",
		Phase: PhaseStorage, Lookup: LookupByName, Resource: inner,
	})

	ctx := t.Context()
	if _, err := wrapped.Get(ctx, Ref{Name: "n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Create(ctx, Spec{Binding: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Update(ctx, Ref{Name: "n"}, Spec{Binding: "B"}); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Delete(ctx, Ref{Name: "n"}); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"resource.get": false, "resource.create": false, "resource.update": false, "resource.delete": false}
	for _, span := range tl.spans.Ended() {
		want[span.Name()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("%s produced no span", name)
		}
	}
}

// Instrument must tolerate nil providers, which is what a real run passes
// before any exporter is configured.
func TestInstrumentWithGlobalProviders(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockResource(ctrl)
	inner.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, nil)

	wrapped := Instrument(nil, nil)(Registration{
		Provider: "p", Type: "t", Capability: "c",
		Phase: PhaseStorage, Lookup: LookupByName, Resource: inner,
	})
	if _, err := wrapped.Get(context.Background(), Ref{Name: "n"}); err != nil {
		t.Fatalf("Get through the global providers: %v", err)
	}
}

// failingMeter builds no instruments, standing in for a metrics pipeline that
// cannot initialise.
type failingMeter struct{ noop.Meter }

func (failingMeter) Int64Histogram(string, ...otelmetric.Int64HistogramOption) (otelmetric.Int64Histogram, error) {
	return nil, errors.New("meter refused to build the instrument")
}

type failingMeterProvider struct{ noop.MeterProvider }

func (failingMeterProvider) Meter(string, ...otelmetric.MeterOption) otelmetric.Meter {
	return failingMeter{}
}

// TestInstrumentSurvivesAFailingMeter: telemetry is for observing a
// deployment, not gating one. A metrics backend that cannot build its
// instrument must leave tracing and the operation itself untouched.
func TestInstrumentSurvivesAFailingMeter(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	inner := NewMockResource(gomock.NewController(t))
	inner.EXPECT().Create(gomock.Any(), gomock.Any()).Return(&State{ID: "made"}, nil)

	wrapped := Instrument(tp, failingMeterProvider{})(Registration{
		Provider: "cloudflare", Type: "r2_bucket", Capability: "objects",
		Phase: PhaseStorage, Lookup: LookupByName, Resource: inner,
	})

	state, err := wrapped.Create(t.Context(), Spec{Binding: "BUCKET"})
	if err != nil {
		t.Fatalf("a failing meter broke the operation: %v", err)
	}
	if state.ID != "made" {
		t.Fatalf("state = %+v", state)
	}
	if len(spans.Ended()) != 1 {
		t.Fatal("a failing meter also suppressed tracing")
	}
}
