/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tracing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// TraceContextAnnotation stores W3C Trace Context as JSON (e.g. {"traceparent":"00-..."}).
	// Used to continue the same trace across Workload reconciles without coupling to Kubernetes Events.
	TraceContextAnnotation = "kueue.x-k8s.io/trace-context"
)

// Instrumenter supports optional OpenTelemetry spans around controller reconciliation.
type Instrumenter interface {
	StartSpan(ctx context.Context, obj metav1.Object, spanName string, attrs map[string]string) (context.Context, func())
	GetTraceContext(ctx context.Context) string
	AddEvent(ctx context.Context, name string, attrs map[string]string)
	// RecordCompletedPhaseSpan records a child span for a completed workload status phase
	// (wall time between start and end). No-op when the current context span is not recording.
	RecordCompletedPhaseSpan(ctx context.Context, phaseName string, start, end time.Time, attrs map[string]string)
	IsRecording(ctx context.Context) bool
}

type noopInstrumenter struct{}

func (n *noopInstrumenter) StartSpan(ctx context.Context, _ metav1.Object, _ string, _ map[string]string) (context.Context, func()) {
	return ctx, func() {}
}

func (n *noopInstrumenter) GetTraceContext(_ context.Context) string { return "" }

func (n *noopInstrumenter) AddEvent(_ context.Context, _ string, _ map[string]string) {}

func (n *noopInstrumenter) RecordCompletedPhaseSpan(_ context.Context, _ string, _, _ time.Time, _ map[string]string) {
}

func (n *noopInstrumenter) IsRecording(_ context.Context) bool { return false }

// NewNoOp returns an Instrumenter that performs no tracing.
func NewNoOp() Instrumenter { return &noopInstrumenter{} }

type otelInstrumenter struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	logger     logr.Logger
}

// StartSpan starts a span, optionally continuing a trace extracted from obj annotations.
func (o *otelInstrumenter) StartSpan(ctx context.Context, obj metav1.Object, spanName string, attrs map[string]string) (context.Context, func()) {
	if obj != nil && obj.GetAnnotations() != nil {
		if tc, ok := obj.GetAnnotations()[TraceContextAnnotation]; ok && tc != "" {
			var carrier map[string]string
			if err := json.Unmarshal([]byte(tc), &carrier); err == nil {
				ctx = o.propagator.Extract(ctx, propagation.MapCarrier(carrier))
			} else {
				o.logger.Error(err, "failed to unmarshal trace context annotation", "annotation", tc)
			}
		}
	}

	opts := []trace.SpanStartOption{}
	if len(attrs) > 0 {
		otelAttrs := make([]attribute.KeyValue, 0, len(attrs))
		for k, v := range attrs {
			otelAttrs = append(otelAttrs, attribute.String(k, v))
		}
		opts = append(opts, trace.WithAttributes(otelAttrs...))
	}

	ctx, span := o.tracer.Start(ctx, spanName, opts...)
	return ctx, func() { span.End() }
}

// GetTraceContext returns the current W3C context as JSON for persistence on API objects.
func (o *otelInstrumenter) GetTraceContext(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	o.propagator.Inject(ctx, carrier)
	data, err := json.Marshal(carrier)
	if err != nil {
		o.logger.Error(err, "failed to marshal trace context")
		return ""
	}
	return string(data)
}

// AddEvent records a span event when the span is recording.
func (o *otelInstrumenter) AddEvent(ctx context.Context, name string, attrs map[string]string) {
	span := trace.SpanFromContext(ctx)
	otelAttrs := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		otelAttrs = append(otelAttrs, attribute.String(k, v))
	}
	span.AddEvent(name, trace.WithAttributes(otelAttrs...))
}

// IsRecording reports whether the current span is recording.
func (o *otelInstrumenter) IsRecording(ctx context.Context) bool {
	return trace.SpanFromContext(ctx).IsRecording()
}

// RecordCompletedPhaseSpan emits a short-lived child span whose timestamps match the
// wall-clock interval spent in phaseName (typically under ReconcileWorkload).
func (o *otelInstrumenter) RecordCompletedPhaseSpan(ctx context.Context, phaseName string, start, end time.Time, attrs map[string]string) {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return
	}
	if end.Before(start) {
		end = start
	}
	spanName := "WorkloadPhase." + phaseName
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(start),
	}
	if len(attrs) > 0 {
		otelAttrs := make([]attribute.KeyValue, 0, len(attrs))
		for k, v := range attrs {
			otelAttrs = append(otelAttrs, attribute.String(k, v))
		}
		opts = append(opts, trace.WithAttributes(otelAttrs...))
	}
	_, span := o.tracer.Start(ctx, spanName, opts...)
	span.End(trace.WithTimestamp(end))
}

// setupConfig configures SDK initialization.
type setupConfig struct {
	samplingRatio float64
	logger        logr.Logger
}

// Option configures SetupOTel.
type Option func(*setupConfig)

// WithSamplingRatio sets the sampler for root spans (no remote parent). Range [0,1].
// 0 disables root sampling; 1 samples all roots. Values in between use trace ID ratio sampling.
func WithSamplingRatio(r float64) Option {
	return func(c *setupConfig) {
		c.samplingRatio = r
	}
}

// WithLogger sets the logger used for tracing setup errors (optional).
func WithLogger(log logr.Logger) Option {
	return func(c *setupConfig) {
		c.logger = log
	}
}

// SetupOTel initializes the global OpenTelemetry SDK and returns an Instrumenter.
// The returned cleanup function should be deferred on shutdown.
// Exporter options follow OTEL_EXPORTER_OTLP_* environment variables.
func SetupOTel(ctx context.Context, serviceName string, opts ...Option) (Instrumenter, func(), error) {
	cfg := setupConfig{
		samplingRatio: 0.01,
		logger:        logr.Discard(),
	}
	for _, o := range opts {
		o(&cfg)
	}

	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("otlp grpc exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("resource: %w", err)
	}

	var rootSampler sdktrace.Sampler
	switch {
	case cfg.samplingRatio <= 0:
		rootSampler = sdktrace.NeverSample()
	case cfg.samplingRatio >= 1:
		rootSampler = sdktrace.AlwaysSample()
	default:
		rootSampler = sdktrace.TraceIDRatioBased(cfg.samplingRatio)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(rootSampler)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	instrumenter := &otelInstrumenter{
		tracer:     tp.Tracer("sigs.k8s.io/kueue"),
		propagator: otel.GetTextMapPropagator(),
		logger:     cfg.logger,
	}
	cleanup := func() {
		shutdownCtx := context.Background()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			cfg.logger.Error(err, "OpenTelemetry tracer provider shutdown")
		}
	}
	return instrumenter, cleanup, nil
}
