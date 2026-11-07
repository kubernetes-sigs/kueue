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

package workload

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// RunDurationDeadline resolves the live override against this admission's default.
func RunDurationDeadline(w *kueue.Workload) *metav1.Time {
	if w.Status.Admission == nil || w.Status.Admission.RunDurationPolicy == nil || w.Status.RunDuration == nil {
		return nil
	}
	seconds := w.Spec.MaximumRunDurationSeconds
	if seconds == nil {
		seconds = w.Status.Admission.RunDurationPolicy.DefaultSeconds
	}
	if seconds == nil {
		return nil
	}
	return new(metav1.NewTime(w.Status.RunDuration.AdmittedAt.Add(time.Duration(*seconds) * time.Second)))
}

// CompleteRunDurationRequeue records a fresh turn only after the job has drained.
func CompleteRunDurationRequeue(w *kueue.Workload, now time.Time) {
	if w.Status.RequeueState == nil {
		w.Status.RequeueState = &kueue.RequeueState{}
	}
	w.Status.RequeueState.QueueOrderTimestamp = new(metav1.NewTime(now))
}
