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

package job

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestStopWaitsForSuspension(t *testing.T) {
	baseJob := utiltestingjob.MakeJob("job", "ns").Suspend(false).
		NodeSelector("pool", "pool-1").NodeSelector("user-restriction", "retained")
	cases := map[string]struct {
		job              *batchv1.Job
		resumeDuringStop bool
		wantWait         bool
	}{
		"no prior acknowledgement": {
			job:      baseJob.Clone().Obj(),
			wantWait: true,
		},
		"resumed job": {
			job: baseJob.Clone().Condition(batchv1.JobCondition{
				Type: batchv1.JobSuspended, Status: corev1.ConditionFalse,
			}).Obj(),
			wantWait: true,
		},
		"stale suspension acknowledgement": {
			job: baseJob.Clone().Condition(batchv1.JobCondition{
				Type: batchv1.JobSuspended, Status: corev1.ConditionTrue,
			}).Obj(),
			wantWait: true,
		},
		"already suspended without acknowledgement": {
			job:      baseJob.Clone().Suspend(true).Obj(),
			wantWait: true,
		},
		"already suspended before acknowledgement": {
			job: baseJob.Clone().Suspend(true).Condition(batchv1.JobCondition{
				Type: batchv1.JobSuspended, Status: corev1.ConditionFalse,
			}).Obj(),
			wantWait: true,
		},
		"already acknowledged": {
			job: baseJob.Clone().Suspend(true).Condition(batchv1.JobCondition{
				Type: batchv1.JobSuspended, Status: corev1.ConditionTrue,
			}).Obj(),
		},
		"explicit native controller": {
			job:      baseJob.Clone().ManagedBy(batchv1.JobControllerName).Obj(),
			wantWait: true,
		},
		"resumed while stopping": {
			job:              baseJob.Clone().Obj(),
			resumeDuringStop: true,
			wantWait:         true,
		},
		"externally managed job": {
			job: baseJob.Clone().ManagedBy("example.com/job-controller").Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().WithObjects(tc.job).WithStatusSubresource(tc.job).Build()
			j := (*Job)(tc.job)
			original := []podset.PodSetInfo{{NodeSelector: map[string]string{"user-restriction": "retained"}, Count: 1}}
			wantStoppedNow := !*tc.job.Spec.Suspend
			stoppedNow, err := j.Stop(ctx, cl, original, jobframework.StopReasonWorkloadEvicted, "")
			if err != nil {
				t.Fatal(err)
			}
			if stoppedNow != wantStoppedNow {
				t.Errorf("Stop() = %v, want %v", stoppedNow, wantStoppedNow)
			}
			if j.IsActive() != tc.wantWait {
				t.Fatalf("IsActive() = %v, want %v before suspension acknowledgement", j.IsActive(), tc.wantWait)
			}
			if tc.wantWait {
				if tc.resumeDuringStop {
					// A resume can race the native controller's acknowledgement.
					tc.job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}
					if err := cl.Status().Update(ctx, tc.job); err != nil {
						t.Fatal(err)
					}
					tc.job.Spec.Suspend = new(false)
					if err := cl.Update(ctx, tc.job); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := j.Stop(ctx, cl, original, jobframework.StopReasonWorkloadEvicted, ""); err != nil {
					t.Fatal(err)
				}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.job), tc.job); err != nil {
					t.Fatal(err)
				}
				if tc.job.Spec.Suspend == nil || !*tc.job.Spec.Suspend || !j.IsActive() {
					t.Fatal("retry must request suspension and retain admission until a fresh acknowledgement")
				}
				if isJobStatusConditionTrue(tc.job.Status.Conditions, batchv1.JobSuspended) {
					t.Fatal("a previous suspension acknowledgement survived the new request")
				}
				tc.job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}
				if err := cl.Status().Update(ctx, tc.job); err != nil {
					t.Fatal(err)
				}
				if _, err := j.Stop(ctx, cl, original, jobframework.StopReasonWorkloadEvicted, ""); err != nil {
					t.Fatal(err)
				}
			}
			if j.IsActive() || !j.IsSuspended() {
				t.Fatal("completed suspension did not permit readmission")
			}
			if diff := cmp.Diff(original[0].NodeSelector, j.Spec.Template.Spec.NodeSelector); diff != "" {
				t.Errorf("restored selectors (-want,+got):\n%s", diff)
			}
			if stoppedNow, err := j.Stop(ctx, cl, original, jobframework.StopReasonWorkloadEvicted, ""); err != nil || stoppedNow {
				t.Fatalf("repeated Stop() = %v, %v; want false, nil", stoppedNow, err)
			}
			if j.IsActive() || !j.IsSuspended() {
				t.Fatal("repeated stop restarted a completed suspension")
			}
		})
	}
}
