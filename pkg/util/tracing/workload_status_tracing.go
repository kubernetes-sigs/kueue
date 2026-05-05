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
	"strconv"
	"time"
)

const (
	// OtelLastWorkloadStatusAnnotation stores JSON {"status":"<pkg/workload summary>","since":"<RFC3339Nano>"}.
	// Used to compute wall-clock time spent in the previous summary status across reconciles (pending | quotaReserved | admitted | finished).
	OtelLastWorkloadStatusAnnotation = "kueue.x-k8s.io/otel-last-workload-status"
)

type otelLastWorkloadStatus struct {
	Status string `json:"status"`
	Since  string `json:"since"`
}

// PrepareWorkloadStatusObservation updates OpenTelemetry when workload.Status(wl) summary changes.
// It emits a WorkloadPhase.<previous> child span (wall time in that status), a WorkloadStatusTransition
// span event (with duration spent in the previous status), and returns a new annotation payload when the
// workload object should be patched.
func PrepareWorkloadStatusObservation(tr Instrumenter, ctx context.Context, now time.Time, annotations map[string]string, currentStatus string) (annotationJSON string, patchAnnotation bool) {
	if tr == nil || !tr.IsRecording(ctx) {
		return "", false
	}

	raw := ""
	if annotations != nil {
		raw = annotations[OtelLastWorkloadStatusAnnotation]
	}

	var prev otelLastWorkloadStatus
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &prev)
	}
	hadAnn := raw != ""

	if !hadAnn || prev.Status == "" {
		b, err := json.Marshal(otelLastWorkloadStatus{
			Status: currentStatus,
			Since:  now.Format(time.RFC3339Nano),
		})
		if err != nil {
			return "", false
		}
		return string(b), true
	}

	if prev.Status != currentStatus {
		sinceTime, err := time.Parse(time.RFC3339Nano, prev.Since)
		if err != nil {
			sinceTime = now
		}
		duration := now.Sub(sinceTime)
		tr.RecordCompletedPhaseSpan(ctx, prev.Status, sinceTime, now, map[string]string{
			"kueue.workload.status.next": currentStatus,
		})
		tr.AddEvent(ctx, "WorkloadStatusTransition", map[string]string{
			"kueue.workload.status.from":                           prev.Status,
			"kueue.workload.status.to":                             currentStatus,
			"kueue.workload.status.duration_in_previous_status_ms": strconv.FormatInt(duration.Milliseconds(), 10),
		})
		b, err := json.Marshal(otelLastWorkloadStatus{
			Status: currentStatus,
			Since:  now.Format(time.RFC3339Nano),
		})
		if err != nil {
			return "", false
		}
		return string(b), true
	}

	return "", false
}
