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

package jobframework

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/podset"
)

type podsReadyTestJob struct {
	obj           client.Object
	podsReady     bool
	podsScheduled bool
}

func (j *podsReadyTestJob) Object() client.Object { return j.obj }
func (j *podsReadyTestJob) IsSuspended() bool     { return false }
func (j *podsReadyTestJob) Suspend()              {}
func (j *podsReadyTestJob) RunWithPodSetsInfo(context.Context, client.Client, []podset.PodSetInfo) error {
	return nil
}
func (j *podsReadyTestJob) RestorePodSetsInfo(context.Context, []podset.PodSetInfo) bool {
	return false
}
func (j *podsReadyTestJob) Finished(context.Context) (string, bool, bool) {
	return "", false, false
}
func (j *podsReadyTestJob) PodSets(context.Context, client.Client) ([]kueue.PodSet, error) {
	return nil, nil
}
func (j *podsReadyTestJob) IsActive() bool { return true }
func (j *podsReadyTestJob) PodsReady(context.Context, client.Client) bool {
	return j.podsReady
}
func (j *podsReadyTestJob) PodsScheduled(context.Context, client.Client) (bool, error) {
	return j.podsScheduled, nil
}
func (j *podsReadyTestJob) GVK() schema.GroupVersionKind {
	return batchv1.SchemeGroupVersion.WithKind("Job")
}

func TestGeneratePodsReadyConditionWaitForScheduling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := testingclock.NewFakeClock(now)
	ctx := t.Context()
	cl := fake.NewClientBuilder().Build()

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns"},
		Status: kueue.WorkloadStatus{
			Admission: &kueue.Admission{},
			Conditions: []metav1.Condition{
				{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
			},
		},
	}

	job := &podsReadyTestJob{
		obj:           &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "ns"}},
		podsReady:     false,
		podsScheduled: false,
	}

	cond := generatePodsReadyCondition(ctx, cl, job, wl, clock)
	if cond.Reason != kueue.WorkloadWaitForScheduling {
		t.Fatalf("reason = %q, want %q", cond.Reason, kueue.WorkloadWaitForScheduling)
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("status = %q, want False", cond.Status)
	}

	job.podsScheduled = true
	cond = generatePodsReadyCondition(ctx, cl, job, wl, clock)
	if cond.Reason != kueue.WorkloadWaitForStart {
		t.Fatalf("reason = %q, want %q", cond.Reason, kueue.WorkloadWaitForStart)
	}
}
