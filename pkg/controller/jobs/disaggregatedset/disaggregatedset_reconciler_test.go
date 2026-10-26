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

package disaggregatedset

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingjobs "sigs.k8s.io/kueue/pkg/util/testingjobs"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

const (
	testNS = "test-ns"
	testDS = "test-ds"
)

func TestReconciler(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	request := reconcile.Request{Name: testDS, Namespace: testNS}
	dsKey := types.NamespacedName{Name: testDS, Namespace: testNS}
	wl0Name := GetWorkloadName(testDS, testDS, 0)
	wl1Name := GetWorkloadName(testDS, testDS, 1)

	baseDS := testingds.MakeDisaggregatedSet(testDS, testNS).
		UID(testDS).
		Queue("lq").
		Roles(
			testingds.MakeRole("prefill").Replicas(2),
			testingds.MakeRole("decode").WithLeader().Size(3),
		)
	baseWorkload := utiltestingapi.MakeWorkload(wl0Name, testNS).
		Queue("lq").
		JobUID(testDS).
		OwnerReference(gvk, testDS, testDS).
		Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
		Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
		Annotation(controllerconstants.JobOwnerNameAnnotation, testDS).
		Annotation(controllerconstants.ComponentWorkloadIndexAnnotation, "0").
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(
			*utiltestingapi.MakePodSet("prefill", 2).RestartPolicy("").Image(utiltestingjobs.TestDefaultContainerImage).Obj(),
			*utiltestingapi.MakePodSet("decode-leader", 1).RestartPolicy("").Image(utiltestingjobs.TestDefaultContainerImage).Obj(),
			*utiltestingapi.MakePodSet("decode-worker", 2).RestartPolicy("").Image(utiltestingjobs.TestDefaultContainerImage).Obj(),
		).
		Priority(0)
	replacingWorkload := baseWorkload.Clone().
		Annotation(podSetReplacementAnnotation, "true").
		Active(false)
	basePod := testingpod.MakePod("test-ds-0-abc-prefill-0", testNS).
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName)

	cases := map[string]struct {
		featureGates  map[featuregate.Feature]bool
		ds            *disaggregatedsetv1.DisaggregatedSet
		workloads     []kueue.Workload
		pods          []corev1.Pod
		wantWorkloads []kueue.Workload
		wantEvents    []utiltesting.EventRecord
	}{
		"creates one Workload per slice": {
			ds: baseDS.Clone().Slices(2).Obj(),
			wantWorkloads: []kueue.Workload{
				*baseWorkload.Clone().Obj(),
				*baseWorkload.Clone().Name(wl1Name).
					Annotation(controllerconstants.ComponentWorkloadIndexAnnotation, "1").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       dsKey,
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message:   fmt.Sprintf("Created Workload: %s/%s", testNS, wl0Name),
				},
				{
					Key:       dsKey,
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message:   fmt.Sprintf("Created Workload: %s/%s", testNS, wl1Name),
				},
			},
		},
		"creates no Workloads for an unmanaged DisaggregatedSet": {
			ds: testingds.MakeDisaggregatedSet(testDS, testNS).
				UID(testDS).
				Roles(testingds.MakeRole("prefill")).
				Obj(),
		},
		"keeps a Workload that matches the spec": {
			ds:            baseDS.Clone().Obj(),
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
		},
		"deletes the Workloads of removed slices": {
			ds: baseDS.Clone().Slices(1).Obj(),
			workloads: []kueue.Workload{
				*baseWorkload.Clone().Obj(),
				*baseWorkload.Clone().Name(wl1Name).
					Annotation(controllerconstants.ComponentWorkloadIndexAnnotation, "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       dsKey,
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonDeletedWorkload,
					Message:   fmt.Sprintf("Deleted Workload: %s/%s", testNS, wl1Name),
				},
			},
		},
		"deletes every Workload when all roles are scaled to zero": {
			ds: testingds.MakeDisaggregatedSet(testDS, testNS).
				UID(testDS).
				Queue("lq").
				Roles(
					testingds.MakeRole("prefill").Replicas(0),
					testingds.MakeRole("decode").WithLeader().Size(3).Replicas(0),
				).
				Obj(),
			workloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       dsKey,
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonDeletedWorkload,
					Message:   fmt.Sprintf("Deleted Workload: %s/%s", testNS, wl0Name),
				},
			},
		},
		"updates the queue name of a Workload without reserved quota": {
			ds:            baseDS.Clone().Queue("lq2").Obj(),
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Queue("lq2").Obj()},
		},
		"keeps the queue name of a Workload with reserved quota": {
			ds: baseDS.Clone().Queue("lq2").Obj(),
			workloads: []kueue.Workload{
				*baseWorkload.Clone().SimpleReserveQuota("cq", "fl", now).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkload.Clone().SimpleReserveQuota("cq", "fl", now).Obj(),
			},
		},
		"propagates the workload-level waitForPodsReady annotation": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadLevelWaitForPodsReady: true},
			ds: baseDS.Clone().
				Annotation(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
				Obj(),
			workloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantWorkloads: []kueue.Workload{
				*baseWorkload.Clone().
					Annotation(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       dsKey,
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   `Updated workload WaitForPodsReady annotation to {"timeoutSeconds":100}`,
				},
			},
		},
		"deactivates a Workload whose PodSet counts no longer match the spec": {
			ds: baseDS.Clone().
				Roles(
					testingds.MakeRole("prefill").Replicas(3),
					testingds.MakeRole("decode").WithLeader().Size(3),
				).
				Obj(),
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*replacingWorkload.Clone().Obj()},
		},
		"leaves a Workload that was deactivated without the replacement annotation": {
			ds: baseDS.Clone().
				Roles(
					testingds.MakeRole("prefill").Replicas(3),
					testingds.MakeRole("decode").WithLeader().Size(3),
				).
				Obj(),
			workloads:     []kueue.Workload{*baseWorkload.Clone().Active(false).Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Active(false).Obj()},
		},
		"waits for the running Pods of the slice before replacing its Workload": {
			ds:            baseDS.Clone().Obj(),
			workloads:     []kueue.Workload{*replacingWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*basePod.Clone().StatusPhase(corev1.PodRunning).Obj()},
			wantWorkloads: []kueue.Workload{*replacingWorkload.Clone().Obj()},
		},
		"deletes a Workload under replacement once the Pods of the slice are gated": {
			ds:        baseDS.Clone().Obj(),
			workloads: []kueue.Workload{*replacingWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*basePod.Clone().Gate(podconstants.SchedulingGateName).Obj(),
				*basePod.Clone().Name("test-ds-0-abc-prefill-1").StatusPhase(corev1.PodFailed).Obj(),
				*basePod.Clone().Name("test-ds-1-abc-prefill-0").
					Label(disaggregatedsetv1.SliceLabelKey, "1").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       dsKey,
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonWorkloadReplaced,
					Message:   fmt.Sprintf("Replacing Workload %s/%s, its PodSets no longer match the spec", testNS, wl0Name),
				},
			},
		},
		"deletes a Workload under replacement when its slice is removed": {
			ds: baseDS.Clone().Obj(),
			workloads: []kueue.Workload{
				*baseWorkload.Clone().Obj(),
				*replacingWorkload.Clone().Name(wl1Name).
					Annotation(controllerconstants.ComponentWorkloadIndexAnnotation, "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       dsKey,
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonDeletedWorkload,
					Message:   fmt.Sprintf("Deleted Workload: %s/%s", testNS, wl1Name),
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(disaggregatedsetv1.AddToScheme)
			indexer := utiltesting.AsIndexer(clientBuilder)

			objs := []client.Object{tc.ds}
			for i := range tc.workloads {
				objs = append(objs, &tc.workloads[i])
			}
			for i := range tc.pods {
				objs = append(objs, &tc.pods[i])
			}
			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := &utiltesting.EventRecorder{}

			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder,
				jobframework.WithIntegrationManager(newTestIntegrationManager(t)))
			if err != nil {
				t.Fatalf("Error creating the reconciler: %v", err)
			}

			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Errorf("Reconcile returned error: %v", err)
			}

			gotWorkloads := &kueue.WorkloadList{}
			if err := kClient.List(ctx, gotWorkloads, client.InNamespace(testNS)); err != nil {
				t.Fatalf("Could not list Workloads after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items,
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.SortSlices(func(a, b kueue.Workload) bool { return a.Name < b.Name }),
			); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Unexpected events (-want,+got):\n%s", diff)
			}
		})
	}
}
