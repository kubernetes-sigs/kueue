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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeInstrumenter struct {
	recording bool
	events    []struct {
		name  string
		attrs map[string]string
	}
	phases []struct {
		name  string
		start time.Time
		end   time.Time
		attrs map[string]string
	}
}

func (f *fakeInstrumenter) StartSpan(ctx context.Context, _ metav1.Object, _ string, _ map[string]string) (context.Context, func()) {
	return ctx, func() {}
}

func (f *fakeInstrumenter) GetTraceContext(context.Context) string { return "" }

func (f *fakeInstrumenter) AddEvent(_ context.Context, name string, attrs map[string]string) {
	cp := make(map[string]string, len(attrs))
	for k, v := range attrs {
		cp[k] = v
	}
	f.events = append(f.events, struct {
		name  string
		attrs map[string]string
	}{name: name, attrs: cp})
}

func (f *fakeInstrumenter) RecordCompletedPhaseSpan(_ context.Context, name string, start, end time.Time, attrs map[string]string) {
	cp := make(map[string]string, len(attrs))
	for k, v := range attrs {
		cp[k] = v
	}
	f.phases = append(f.phases, struct {
		name  string
		start time.Time
		end   time.Time
		attrs map[string]string
	}{name: name, start: start, end: end, attrs: cp})
}

func (f *fakeInstrumenter) IsRecording(context.Context) bool { return f.recording }

func TestPrepareWorkloadStatusObservation_noOpWhenNotRecording(t *testing.T) {
	f := &fakeInstrumenter{recording: false}
	ctx := context.Background()
	now := time.Unix(1000, 0)
	got, patch := PrepareWorkloadStatusObservation(f, ctx, now, nil, "pending")
	if patch || got != "" {
		t.Fatalf("want no patch when not recording, got patch=%v json=%q", patch, got)
	}
}

func TestPrepareWorkloadStatusObservation_firstObservation(t *testing.T) {
	f := &fakeInstrumenter{recording: true}
	ctx := context.Background()
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	got, patch := PrepareWorkloadStatusObservation(f, ctx, now, nil, "pending")
	if !patch || got == "" {
		t.Fatalf("want patch on first observation")
	}
	var payload otelLastWorkloadStatus
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "pending" {
		t.Fatalf("status: got %q", payload.Status)
	}
	if len(f.events) != 0 {
		t.Fatalf("first observation should not emit events, got %v", f.events)
	}
}

func TestPrepareWorkloadStatusObservation_sameStatusNoPatch(t *testing.T) {
	f := &fakeInstrumenter{recording: true}
	ctx := context.Background()
	t0 := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	prev, _ := json.Marshal(otelLastWorkloadStatus{Status: "pending", Since: t0.Format(time.RFC3339Nano)})
	ann := map[string]string{OtelLastWorkloadStatusAnnotation: string(prev)}
	got, patch := PrepareWorkloadStatusObservation(f, ctx, t0.Add(time.Minute), ann, "pending")
	if patch || got != "" {
		t.Fatalf("want no patch when status unchanged")
	}
}

func TestPrepareWorkloadStatusObservation_transitionEmitsEventAndDuration(t *testing.T) {
	f := &fakeInstrumenter{recording: true}
	ctx := context.Background()
	t0 := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(90 * time.Second)
	prev, _ := json.Marshal(otelLastWorkloadStatus{Status: "pending", Since: t0.Format(time.RFC3339Nano)})
	ann := map[string]string{OtelLastWorkloadStatusAnnotation: string(prev)}
	got, patch := PrepareWorkloadStatusObservation(f, ctx, t1, ann, "quotaReserved")
	if !patch || got == "" {
		t.Fatalf("want patch on transition")
	}
	if len(f.phases) != 1 || f.phases[0].name != "pending" {
		t.Fatalf("want one completed phase span for pending, got %#v", f.phases)
	}
	if !f.phases[0].start.Equal(t0) || !f.phases[0].end.Equal(t1) {
		t.Fatalf("phase span times: start=%v end=%v want start=%v end=%v", f.phases[0].start, f.phases[0].end, t0, t1)
	}
	if f.phases[0].attrs["kueue.workload.status.next"] != "quotaReserved" {
		t.Fatalf("phase attrs: %v", f.phases[0].attrs)
	}
	if len(f.events) != 1 || f.events[0].name != "WorkloadStatusTransition" {
		t.Fatalf("want one WorkloadStatusTransition event, got %#v", f.events)
	}
	attrs := f.events[0].attrs
	if attrs["kueue.workload.status.from"] != "pending" || attrs["kueue.workload.status.to"] != "quotaReserved" {
		t.Fatalf("attrs: %v", attrs)
	}
	if attrs["kueue.workload.status.duration_in_previous_status_ms"] != "90000" {
		t.Fatalf("duration ms: got %q", attrs["kueue.workload.status.duration_in_previous_status_ms"])
	}
	var payload otelLastWorkloadStatus
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "quotaReserved" {
		t.Fatalf("annotation status: %q", payload.Status)
	}
}
