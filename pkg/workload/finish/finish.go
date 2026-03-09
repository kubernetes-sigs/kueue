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

package finish

import (
	"context"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload/patching"
)

func SetFinishedCondition(w *kueue.Workload, now time.Time, reason string, message string) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadFinished,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

// IsFinished returns true if the workload is finished.
func IsFinished(w *kueue.Workload) bool {
	return apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadFinished)
}

type FinishOption func(*FinishOptions)

type FinishOptions struct {
	Clock       clock.Clock
	RoleTracker *roletracker.RoleTracker
}

func DefaultFinishOptions() *FinishOptions {
	return &FinishOptions{
		Clock: clock.RealClock{},
	}
}

func WithClock(clock clock.Clock) FinishOption {
	return func(o *FinishOptions) {
		o.Clock = clock
	}
}

func WithRoleTracker(tracker *roletracker.RoleTracker) FinishOption {
	return func(o *FinishOptions) {
		o.RoleTracker = tracker
	}
}

func Finish(ctx context.Context, c client.Client, wl *kueue.Workload, reason, msg string, options ...FinishOption) error {
	opts := DefaultFinishOptions()
	for _, opt := range options {
		opt(opts)
	}

	if IsFinished(wl) {
		return nil
	}
	if err := patch.PatchAdmissionStatus(ctx, c, wl, opts.Clock, func(wl *kueue.Workload) (bool, error) {
		return SetFinishedCondition(wl, opts.Clock.Now(), reason, msg), nil
	}); err != nil {
		return err
	}
	priorityClassName := patch.PriorityClassName(wl)
	metrics.IncrementFinishedWorkloadTotal(ptr.Deref(wl.Status.Admission, kueue.Admission{}).ClusterQueue, priorityClassName, opts.RoleTracker)
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.IncrementLocalQueueFinishedWorkloadTotal(metrics.LQRefFromWorkload(wl), priorityClassName, opts.RoleTracker)
	}
	return nil
}
