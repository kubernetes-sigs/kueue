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
	"slices"
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
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingjobs "sigs.k8s.io/kueue/pkg/util/testingjobs"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingsts "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
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

	// The child objects of slice 0, as the DisaggregatedSet and LeaderWorkerSet
	// controllers create them.
	const (
		prefillLWS = "test-ds-0-rev-prefill"
		decodeLWS  = "test-ds-0-rev-decode"
	)
	prefillLWSObj := testinglws.MakeLeaderWorkerSet(prefillLWS, testNS).
		UID(prefillLWS).
		OwnerReferenceWithUID(testDS, gvk, testDS).
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Label(disaggregatedsetv1.RoleLabelKey, "prefill").
		Label(disaggregatedsetv1.RevisionLabelKey, "rev")
	prefillSTS := testingsts.MakeStatefulSet(prefillLWS, testNS).
		UID(prefillLWS).
		OwnerReferenceWithUID(prefillLWS, leaderWorkerSetGVK, prefillLWS).
		Replicas(2)
	ungatedPrefillPod := testingpod.MakePod(prefillLWS+"-0", testNS).
		UID(prefillLWS+"-0").
		OwnerReferenceWithUID(prefillLWS, statefulSetGVK, prefillLWS).
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Label(disaggregatedsetv1.RoleLabelKey, "prefill").
		Label(disaggregatedsetv1.RevisionLabelKey, "rev").
		SuspendedByParent(FrameworkName)
	prefillPod := ungatedPrefillPod.Clone().Gate(podconstants.SchedulingGateName)
	ungatedAdoptedPrefillPod := ungatedPrefillPod.Clone().
		ManagedByKueueLabel().
		Queue("lq").
		GroupNameAnnotation(wl0Name).
		PrebuiltWorkloadAnnotation(wl0Name).
		GroupTotalCount("5").
		RoleHash("prefill")
	adoptedPrefillPod := ungatedAdoptedPrefillPod.Clone().Gate(podconstants.SchedulingGateName)
	// The new revision of the prefill role, during a rollout.
	const prefillRev2LWS = "test-ds-0-rev2-prefill"
	prefillRev2LWSObj := testinglws.MakeLeaderWorkerSet(prefillRev2LWS, testNS).
		UID(prefillRev2LWS).
		OwnerReferenceWithUID(testDS, gvk, testDS).
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Label(disaggregatedsetv1.RoleLabelKey, "prefill").
		Label(disaggregatedsetv1.RevisionLabelKey, "rev2")
	prefillRev2STS := testingsts.MakeStatefulSet(prefillRev2LWS, testNS).
		UID(prefillRev2LWS).
		OwnerReferenceWithUID(prefillRev2LWS, leaderWorkerSetGVK, prefillRev2LWS).
		Replicas(2)
	prefillRev2Pod := testingpod.MakePod(prefillRev2LWS+"-0", testNS).
		UID(prefillRev2LWS+"-0").
		OwnerReferenceWithUID(prefillRev2LWS, statefulSetGVK, prefillRev2LWS).
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Label(disaggregatedsetv1.RoleLabelKey, "prefill").
		Label(disaggregatedsetv1.RevisionLabelKey, "rev2").
		SuspendedByParent(FrameworkName).
		Gate(podconstants.SchedulingGateName)
	terminatingPrefillPod := ungatedAdoptedPrefillPod.Clone().
		NodeName("node").
		KueueFinalizer().
		DeletionTimestamp(now)
	runningPrefillPod := ungatedAdoptedPrefillPod.Clone().
		Name(prefillLWS + "-1").
		UID(prefillLWS + "-1").
		NodeName("node").
		StatusPhase(corev1.PodRunning)
	decodeLWSObj := testinglws.MakeLeaderWorkerSet(decodeLWS, testNS).
		UID(decodeLWS).
		OwnerReferenceWithUID(testDS, gvk, testDS).
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Label(disaggregatedsetv1.RoleLabelKey, "decode").
		Label(disaggregatedsetv1.RevisionLabelKey, "rev")
	decodeLeaderSTS := testingsts.MakeStatefulSet(decodeLWS, testNS).
		UID(decodeLWS).
		OwnerReferenceWithUID(decodeLWS, leaderWorkerSetGVK, decodeLWS).
		Replicas(1)
	decodeLeaderPod := testingpod.MakePod(decodeLWS+"-0", testNS).
		UID(decodeLWS+"-0").
		OwnerReferenceWithUID(decodeLWS, statefulSetGVK, decodeLWS).
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Label(disaggregatedsetv1.RoleLabelKey, "decode").
		Label(disaggregatedsetv1.RevisionLabelKey, "rev").
		Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
		SuspendedByParent(FrameworkName).
		Gate(podconstants.SchedulingGateName)
	decodeWorkerSTS := testingsts.MakeStatefulSet(decodeLWS+"-0", testNS).
		UID(decodeLWS+"-0-workers").
		OwnerReferenceWithUID(decodeLWS+"-0", podGVK, decodeLWS+"-0").
		OrdinalsStart(1).
		Replicas(2)
	decodeWorkerPod := testingpod.MakePod(decodeLWS+"-0-1", testNS).
		UID(decodeLWS+"-0-1").
		OwnerReferenceWithUID(decodeLWS+"-0", statefulSetGVK, decodeLWS+"-0-workers").
		Label(disaggregatedsetv1.SetNameLabelKey, testDS).
		Label(disaggregatedsetv1.SliceLabelKey, "0").
		Label(disaggregatedsetv1.RoleLabelKey, "decode").
		Label(disaggregatedsetv1.RevisionLabelKey, "rev").
		Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
		SuspendedByParent(FrameworkName).
		Gate(podconstants.SchedulingGateName)

	cases := map[string]struct {
		featureGates  map[featuregate.Feature]bool
		ds            *disaggregatedsetv1.DisaggregatedSet
		owners        []client.Object
		workloads     []kueue.Workload
		pods          []corev1.Pod
		wantWorkloads []kueue.Workload
		wantPods      []corev1.Pod
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
			wantPods:      []corev1.Pod{*basePod.Clone().StatusPhase(corev1.PodRunning).Obj()},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "test-ds-0-abc-prefill-0", Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    ReasonPodNotAdopted,
					Message:   "Not adopted by DisaggregatedSet test-ds: test-ds-0-abc-prefill-0 is not controlled by a StatefulSet",
				},
			},
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
			wantPods: []corev1.Pod{
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
				{
					Key:       types.NamespacedName{Name: "test-ds-0-abc-prefill-0", Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    ReasonPodNotAdopted,
					Message:   "Not adopted by DisaggregatedSet test-ds: test-ds-0-abc-prefill-0 is not controlled by a StatefulSet",
				},
				{
					Key:       types.NamespacedName{Name: "test-ds-1-abc-prefill-0", Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    ReasonPodNotAdopted,
					Message:   "Not adopted by DisaggregatedSet test-ds: test-ds-1-abc-prefill-0 is not controlled by a StatefulSet",
				},
			},
		},
		"adopts the Pods of every role into the pod group of their slice": {
			ds:            baseDS.Clone().Obj(),
			owners:        []client.Object{prefillLWSObj.Obj(), prefillSTS.Obj(), decodeLWSObj.Obj(), decodeLeaderSTS.Obj(), decodeWorkerSTS.Obj()},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*prefillPod.Clone().Obj(), *decodeLeaderPod.Clone().Obj(), *decodeWorkerPod.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods: []corev1.Pod{
				*adoptedPrefillPod.Clone().Obj(),
				*decodeLeaderPod.Clone().
					ManagedByKueueLabel().
					Queue("lq").
					GroupNameAnnotation(wl0Name).
					PrebuiltWorkloadAnnotation(wl0Name).
					GroupTotalCount("5").
					RoleHash("decode-leader").
					Obj(),
				*decodeWorkerPod.Clone().
					ManagedByKueueLabel().
					Queue("lq").
					GroupNameAnnotation(wl0Name).
					PrebuiltWorkloadAnnotation(wl0Name).
					GroupTotalCount("5").
					RoleHash("decode-worker").
					Obj(),
			},
		},
		"sets the new queue name only on adopted Pods that are still gated": {
			ds:        baseDS.Clone().Queue("lq2").Obj(),
			owners:    []client.Object{prefillLWSObj.Obj(), prefillSTS.Obj()},
			workloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*adoptedPrefillPod.Clone().Obj(),
				*ungatedAdoptedPrefillPod.Clone().Name(prefillLWS + "-1").UID(prefillLWS + "-1").StatusPhase(corev1.PodRunning).Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Queue("lq2").Obj()},
			wantPods: []corev1.Pod{
				*adoptedPrefillPod.Clone().Queue("lq2").Obj(),
				*ungatedAdoptedPrefillPod.Clone().Name(prefillLWS + "-1").UID(prefillLWS + "-1").StatusPhase(corev1.PodRunning).Obj(),
			},
		},
		"does not adopt a Pod whose labels do not match its LeaderWorkerSet": {
			ds:            baseDS.Clone().Obj(),
			owners:        []client.Object{prefillLWSObj.Obj(), prefillSTS.Obj()},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*prefillPod.Clone().Label(disaggregatedsetv1.RevisionLabelKey, "forged").Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods:      []corev1.Pod{*prefillPod.Clone().Label(disaggregatedsetv1.RevisionLabelKey, "forged").Obj()},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: prefillLWS + "-0", Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    ReasonPodNotAdopted,
					Message: fmt.Sprintf("Not adopted by DisaggregatedSet test-ds: label %s does not match LeaderWorkerSet %s",
						disaggregatedsetv1.RevisionLabelKey, prefillLWS),
				},
			},
		},
		"does not adopt a Pod whose ordinal is outside the StatefulSet replicas": {
			ds:            baseDS.Clone().Obj(),
			owners:        []client.Object{prefillLWSObj.Obj(), prefillSTS.Obj()},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*prefillPod.Clone().Name(prefillLWS + "-2").Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods:      []corev1.Pod{*prefillPod.Clone().Name(prefillLWS + "-2").Obj()},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: prefillLWS + "-2", Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    ReasonPodNotAdopted,
					Message:   fmt.Sprintf("Not adopted by DisaggregatedSet test-ds: ordinal 2 is outside the replicas of StatefulSet %s", prefillLWS),
				},
			},
		},
		"does not adopt a Pod whose LeaderWorkerSet belongs to another DisaggregatedSet": {
			ds: baseDS.Clone().Obj(),
			owners: []client.Object{
				testinglws.MakeLeaderWorkerSet(prefillLWS, testNS).
					UID(prefillLWS).
					OwnerReferenceWithUID(testDS, gvk, "other-ds").
					Label(disaggregatedsetv1.SetNameLabelKey, testDS).
					Label(disaggregatedsetv1.SliceLabelKey, "0").
					Label(disaggregatedsetv1.RoleLabelKey, "prefill").
					Label(disaggregatedsetv1.RevisionLabelKey, "rev").
					Obj(),
				prefillSTS.Obj(),
			},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*prefillPod.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods:      []corev1.Pod{*prefillPod.Clone().Obj()},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: prefillLWS + "-0", Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    ReasonPodNotAdopted,
					Message:   fmt.Sprintf("Not adopted by DisaggregatedSet test-ds: LeaderWorkerSet %s is not controlled by the DisaggregatedSet", prefillLWS),
				},
			},
		},
		"ungates a Pod whose StatefulSet is gone": {
			ds:            baseDS.Clone().Obj(),
			owners:        []client.Object{prefillLWSObj.Obj()},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*prefillPod.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods:      []corev1.Pod{*ungatedPrefillPod.Clone().Obj()},
		},
		"ungates a Pod whose StatefulSet was recreated with another UID": {
			ds: baseDS.Clone().Obj(),
			owners: []client.Object{
				prefillLWSObj.Obj(),
				testingsts.MakeStatefulSet(prefillLWS, testNS).
					UID("recreated").
					OwnerReferenceWithUID(prefillLWS, leaderWorkerSetGVK, prefillLWS).
					Replicas(2).
					Obj(),
			},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*prefillPod.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods:      []corev1.Pod{*ungatedPrefillPod.Clone().Obj()},
		},
		"ungates an adopted Pod whose owner reference was cleared": {
			ds:            baseDS.Clone().Obj(),
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*basePod.Clone().ManagedByKueueLabel().Gate(podconstants.SchedulingGateName).Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods:      []corev1.Pod{*basePod.Clone().ManagedByKueueLabel().Obj()},
		},
		"ungates an adopted Pod whose StatefulSet lost the reference to its LeaderWorkerSet": {
			ds: baseDS.Clone().Obj(),
			owners: []client.Object{
				prefillLWSObj.Obj(),
				testingsts.MakeStatefulSet(prefillLWS, testNS).UID(prefillLWS).Replicas(2).Obj(),
			},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*adoptedPrefillPod.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods:      []corev1.Pod{*ungatedAdoptedPrefillPod.Clone().Obj()},
		},
		"does not adopt a new-revision Pod while the Pod it replaces is terminating on a node": {
			ds:        baseDS.Clone().Obj(),
			owners:    []client.Object{prefillLWSObj.Obj(), prefillSTS.Obj(), prefillRev2LWSObj.Obj(), prefillRev2STS.Obj()},
			workloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*terminatingPrefillPod.Clone().Obj(),
				*runningPrefillPod.Clone().Obj(),
				*prefillRev2Pod.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods: []corev1.Pod{
				*terminatingPrefillPod.Clone().Obj(),
				*runningPrefillPod.Clone().Obj(),
				*prefillRev2Pod.Clone().Obj(),
			},
		},
		"adopts a new-revision Pod once the Pod it replaces is gone": {
			ds:            baseDS.Clone().Obj(),
			owners:        []client.Object{prefillLWSObj.Obj(), prefillSTS.Obj(), prefillRev2LWSObj.Obj(), prefillRev2STS.Obj()},
			workloads:     []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods:          []corev1.Pod{*runningPrefillPod.Clone().Obj(), *prefillRev2Pod.Clone().Obj()},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods: []corev1.Pod{
				*runningPrefillPod.Clone().Obj(),
				*prefillRev2Pod.Clone().
					ManagedByKueueLabel().
					Queue("lq").
					GroupNameAnnotation(wl0Name).
					PrebuiltWorkloadAnnotation(wl0Name).
					GroupTotalCount("5").
					RoleHash("prefill").
					Obj(),
			},
		},
		"adopts the oldest Pods first when the slice has fewer free slots": {
			ds:        baseDS.Clone().Obj(),
			owners:    []client.Object{prefillLWSObj.Obj(), prefillSTS.Obj(), prefillRev2LWSObj.Obj(), prefillRev2STS.Obj()},
			workloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*prefillRev2Pod.Clone().CreationTimestamp(now).Obj(),
				*prefillPod.Clone().Name(prefillLWS + "-1").UID(prefillLWS + "-1").CreationTimestamp(now.Add(time.Second)).Obj(),
				*prefillPod.Clone().CreationTimestamp(now.Add(2 * time.Second)).Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkload.Clone().Obj()},
			wantPods: []corev1.Pod{
				*prefillRev2Pod.Clone().CreationTimestamp(now).
					ManagedByKueueLabel().
					Queue("lq").
					GroupNameAnnotation(wl0Name).
					PrebuiltWorkloadAnnotation(wl0Name).
					GroupTotalCount("5").
					RoleHash("prefill").
					Obj(),
				*adoptedPrefillPod.Clone().Name(prefillLWS + "-1").UID(prefillLWS + "-1").CreationTimestamp(now.Add(time.Second)).Obj(),
				*prefillPod.Clone().CreationTimestamp(now.Add(2 * time.Second)).Obj(),
			},
		},
		"ungates the Pods of a deleted DisaggregatedSet": {
			pods:     []corev1.Pod{*adoptedPrefillPod.Clone().Obj()},
			wantPods: []corev1.Pod{*ungatedAdoptedPrefillPod.Clone().Obj()},
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
			clientBuilder := utiltesting.NewClientBuilder(disaggregatedsetv1.AddToScheme, leaderworkersetv1.AddToScheme)
			indexer := utiltesting.AsIndexer(clientBuilder)

			objs := slices.Clone(tc.owners)
			if tc.ds != nil {
				objs = append(objs, tc.ds)
			}
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
			gotPods := &corev1.PodList{}
			if err := kClient.List(ctx, gotPods, client.InNamespace(testNS)); err != nil {
				t.Fatalf("Could not list Pods after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantPods, gotPods.Items,
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.SortSlices(func(a, b corev1.Pod) bool { return a.Name < b.Name }),
			); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Unexpected events (-want,+got):\n%s", diff)
			}
		})
	}
}
