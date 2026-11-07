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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestJobRunDurationDrained(t *testing.T) {
	cases := map[string]struct {
		phase      corev1.PodPhase
		active     int32
		noPod      bool
		otherOwner bool
		noAck      bool
		want       bool
	}{
		"running":             {phase: corev1.PodRunning},
		"pending":             {phase: corev1.PodPending},
		"unknown":             {},
		"terminal":            {phase: corev1.PodFailed, want: true},
		"no pods":             {noPod: true, want: true},
		"status still active": {noPod: true, active: 1},
		"empty list is not suspension acknowledgement": {noPod: true, noAck: true},
		"unrelated pod does not hold quota":            {phase: corev1.PodRunning, otherOwner: true, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			job := testingjob.MakeJob("job", "ns").UID("job-uid").Suspend(true).
				SetAnnotation(StoppingAnnotation, runDurationSchedulingRestored).Obj()
			job.Status.Active = tc.active
			if !tc.noAck {
				job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}
			}
			objects := []client.Object{job}
			if !tc.noPod {
				ownerUID := "job-uid"
				if tc.otherOwner {
					ownerUID = "another-job"
				}
				pod := testingpod.MakePod("pod", "ns").Label(batchv1.ControllerUidLabel, ownerUID).StatusPhase(tc.phase).Obj()
				objects = append(objects, pod)
			}
			c := utiltesting.NewFakeClient(objects...)
			got, err := (*Job)(job).RunDurationDrained(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("drained = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJobRunDurationStopAcknowledgement(t *testing.T) {
	job := testingjob.MakeJob("job", "ns").UID("uid").Suspend(false).Obj()
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}
	c := utiltesting.NewFakeClient(job)
	j := (*Job)(job)
	reason := jobframework.StopReason(string(jobframework.StopReasonWorkloadEvicted) + "DueTo" + kueue.WorkloadEvictedByRunDuration)
	if _, err := j.Stop(t.Context(), c, nil, reason, "duration expired"); err != nil {
		t.Fatal(err)
	}
	if isJobStatusConditionTrue(j.Status.Conditions, batchv1.JobSuspended) {
		t.Fatal("a previous run's suspension acknowledgement was retained")
	}
	if drained, err := j.RunDurationDrained(t.Context(), c); err != nil || drained {
		t.Fatalf("released without fresh acknowledgement: drained=%v, error=%v", drained, err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(job), job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Stop(t.Context(), c, nil, reason, "duration expired"); err != nil {
		t.Fatal(err)
	}
	if drained, err := j.RunDurationDrained(t.Context(), c); err != nil || !drained {
		t.Fatalf("repeated stop discarded fresh acknowledgement: drained=%v, error=%v", drained, err)
	}
}
