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
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestUpdateWaitForPodsReady(t *testing.T) {
	const (
		jobName   = "test"
		namespace = "ns"
	)
	workloadName := jobName

	cases := map[string]struct {
		// workload is the state stored in the API server before the call.
		workload *kueue.Workload
		// job is the owner object whose annotation drives propagation.
		job       *batchv1.Job
		wantSpec  *kueue.WaitForPodsReady
		wantEvent *utiltesting.EventRecord
	}{
		"annotation on workload, none on job, spec already nil": {
			workload: utiltestingapi.MakeWorkload(workloadName, namespace).
				Annotation(controllerconsts.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
				Obj(),
			job:      testingjob.MakeJob(jobName, namespace).Obj(),
			wantSpec: nil,
			wantEvent: &utiltesting.EventRecord{
				Key:       types.NamespacedName{Namespace: namespace, Name: workloadName},
				EventType: corev1.EventTypeNormal,
				Reason:    ReasonUpdatedWorkload,
				Message:   "Cleared workload WaitForPodsReady",
			},
		},
		"redundant annotation removed silently when spec already matches": {
			workload: utiltestingapi.MakeWorkload(workloadName, namespace).
				Annotation(controllerconsts.WaitForPodsReadyAnnotation, `{"timeoutSeconds":50}`).
				WaitForPodsReady(&kueue.WaitForPodsReady{TimeoutSeconds: 50}).
				Obj(),
			job: testingjob.MakeJob(jobName, namespace).
				SetAnnotation(controllerconsts.WaitForPodsReadyAnnotation, `{"timeoutSeconds":50}`).
				Obj(),
			wantSpec:  &kueue.WaitForPodsReady{TimeoutSeconds: 50},
			wantEvent: nil,
		},
		"no annotation on workload and job; no event": {
			workload: utiltestingapi.MakeWorkload(workloadName, namespace).
				Obj(),
			job: testingjob.MakeJob(jobName, namespace).
				Obj(),
			wantSpec:  nil,
			wantEvent: nil,
		},
		"no annotation on workload, job annotation changes": {
			workload: utiltestingapi.MakeWorkload(workloadName, namespace).
				WaitForPodsReady(&kueue.WaitForPodsReady{TimeoutSeconds: 50}).
				Obj(),
			job: testingjob.MakeJob(jobName, namespace).
				SetAnnotation(controllerconsts.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
				Obj(),
			wantSpec: &kueue.WaitForPodsReady{TimeoutSeconds: 100},
			wantEvent: &utiltesting.EventRecord{
				Key:       types.NamespacedName{Namespace: namespace, Name: workloadName},
				EventType: corev1.EventTypeNormal,
				Reason:    ReasonUpdatedWorkload,
				Message:   "Updated workload spec WaitForPodsReady to timeoutSeconds=100",
			},
		},
		"no annotation on workload, job annotation removed, spec cleared": {
			workload: utiltestingapi.MakeWorkload(workloadName, namespace).
				WaitForPodsReady(&kueue.WaitForPodsReady{TimeoutSeconds: 50}).
				Obj(),
			job: testingjob.MakeJob(jobName, namespace).Obj(),
			// The job no longer configures WaitForPodsReady, so the spec is cleared to nil.
			// This is a user-visible loss of configuration, so it is reported as a Cleared event.
			wantSpec: nil,
			wantEvent: &utiltesting.EventRecord{
				Key:       types.NamespacedName{Namespace: namespace, Name: workloadName},
				EventType: corev1.EventTypeNormal,
				Reason:    ReasonUpdatedWorkload,
				Message:   "Cleared workload WaitForPodsReady",
			},
		},
		"upgrade: legacy annotation on workload and job, nil spec; migrated to spec": {
			workload: utiltestingapi.MakeWorkload(workloadName, namespace).
				Annotation(controllerconsts.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
				Obj(),
			job: testingjob.MakeJob(jobName, namespace).
				SetAnnotation(controllerconsts.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
				Obj(),
			wantSpec: &kueue.WaitForPodsReady{TimeoutSeconds: 100},
			wantEvent: &utiltesting.EventRecord{
				Key:       types.NamespacedName{Namespace: namespace, Name: workloadName},
				EventType: corev1.EventTypeNormal,
				Reason:    ReasonUpdatedWorkload,
				Message:   "Updated workload spec WaitForPodsReady to timeoutSeconds=100",
			},
		},
		"unparseable job annotation is ignored; spec unchanged and no event": {
			workload: utiltestingapi.MakeWorkload(workloadName, namespace).
				Obj(),
			job: testingjob.MakeJob(jobName, namespace).
				SetAnnotation(controllerconsts.WaitForPodsReadyAnnotation, `{"timeoutSeconds":`).
				Obj(),
			wantSpec:  nil,
			wantEvent: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.WorkloadLevelWaitForPodsReady, true)
			ctx, _ := utiltesting.ContextWithLog(t)

			cl := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).
				WithObjects(tc.workload).
				Build()

			// Read afresh the way a reconcile would, so a regression that only
			// mutates the in-memory copy cannot pass.
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: workloadName}, wl); err != nil {
				t.Fatalf("getting workload: %v", err)
			}

			recorder := &utiltesting.EventRecorder{}
			if err := UpdateWaitForPodsReady(ctx, cl, recorder, tc.job, wl); err != nil {
				t.Fatalf("UpdateWaitForPodsReady: %v", err)
			}

			persisted := &kueue.Workload{}
			if err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: workloadName}, persisted); err != nil {
				t.Fatalf("re-getting workload: %v", err)
			}

			// The legacy annotation is always cleared once its configuration is reconciled into the spec.
			if _, hasAnnotation := persisted.Annotations[controllerconsts.WaitForPodsReadyAnnotation]; hasAnnotation {
				t.Errorf("legacy WaitForPodsReady annotation should be absent after sync, but it is present")
			}
			if diff := cmp.Diff(tc.wantSpec, persisted.Spec.WaitForPodsReady); diff != "" {
				t.Errorf("persisted Spec.WaitForPodsReady mismatch (-want +got):\n%s", diff)
			}

			var wantEvents []utiltesting.EventRecord
			if tc.wantEvent != nil {
				wantEvents = []utiltesting.EventRecord{*tc.wantEvent}
			}
			if diff := cmp.Diff(wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("recorded events mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
