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

package leaderworkerset

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingjobs "sigs.k8s.io/kueue/pkg/util/testingjobs"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	baseCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
)

const (
	testNS  = "test-ns"
	testLWS = "test-lws"
	testSTS = "test-sts"
)

var (
	stsGVK = appsv1.SchemeGroupVersion.WithKind("StatefulSet")
)

func TestReconciler(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	request := reconcile.Request{Name: testLWS, Namespace: testNS}
	workloadUpdateErr := errors.New("workload update failed")
	topologyLWS := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Queue("lq").
		Replicas(2).Size(2).RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
		LeaderTemplate(corev1.PodTemplateSpec{}).
		LeaderTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
		WorkerTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname)
	updatedTopologyLWS := topologyLWS.Obj()
	partitionedLWS := updatedTopologyLWS.DeepCopy()
	partitionedLWS.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{Partition: new(int32(1))}
	rollingLeader := statefulset.MakeStatefulSet(testLWS, testNS).Label(leaderworkersetv1.SetNameLabelKey, testLWS).Obj()
	rollingLeader.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(updatedTopologyLWS, gvk)}
	rollingLeader.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{Partition: new(int32(1))}
	rollingLeader.Spec.Template = *updatedTopologyLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.DeepCopy()
	rollingLeader.Spec.Template.Labels = map[string]string{leaderworkersetv1.RevisionKey: "updated"}
	leaderBeforeRollout := rollingLeader.DeepCopy()
	leaderBeforeRollout.Spec.UpdateStrategy.RollingUpdate.Partition = new(int32(0))
	leaderBeforeRollout.Labels[leaderworkersetv1.RevisionKey] = "old"
	leaderBeforeRollout.Spec.Template.Labels = map[string]string{leaderworkersetv1.RevisionKey: "old"}
	unobservedTopologyLWS := topologyLWS.Clone().Replicas(1).Generation(2).ObservedGeneration(1)
	unobservedLWS := unobservedTopologyLWS.Obj()
	observedLWS := topologyLWS.Clone().Generation(2).ObservedGeneration(2).Obj()
	previousLWS := unobservedTopologyLWS.Clone().
		WorkerTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).Obj()
	oldRevision, err := leaderworkerset.MakeControllerRevision(previousLWS, "old")
	if err != nil {
		t.Fatal(err)
	}
	updatedRevision, err := leaderworkerset.MakeControllerRevision(observedLWS, "updated")
	if err != nil {
		t.Fatal(err)
	}
	reservedGroup := utiltestingapi.MakeWorkload("", testNS).
		Queue("lq").Group().OwnerReference(gvk, testLWS, testLWS).
		PodSets(
			*utiltestingapi.MakePodSet(workerPodSetName, 1).
				RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).Obj(),
			*utiltestingapi.MakePodSet(leaderPodSetName, 1).RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).Obj(),
		).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(false, now).
		AdmissionCheck(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending})
	reservedGroup0 := reservedGroup.Clone().Name(GetWorkloadName(testLWS, testLWS, "0")).UID("wl-0").
		Annotation(constants.ComponentWorkloadIndexAnnotation, "0").Obj()
	reservedGroup1 := reservedGroup.Clone().Name(GetWorkloadName(testLWS, testLWS, "1")).UID("wl-1").
		Annotation(constants.ComponentWorkloadIndexAnnotation, "1").Obj()
	unreservedLWS := topologyLWS.Clone().Replicas(1).Obj()
	unreservedGroup := reservedGroup0.DeepCopy()
	unreservedGroup.Status = kueue.WorkloadStatus{}
	refreshedGroup := unreservedGroup.DeepCopy()
	refreshedGroup.Spec.PodSets[0].TopologyRequest.Required = new(corev1.LabelHostname)
	deletedGroup1Event := utiltesting.EventRecord{
		Key:       request.NamespacedName,
		EventType: corev1.EventTypeNormal,
		Reason:    jobframework.ReasonDeletedWorkload,
		Message:   fmt.Sprintf("Deleted Workload %s because its reserved topology request no longer matches the LeaderWorkerSet template", workload.Key(reservedGroup1)),
	}
	boundPendingPod := testingjobspod.MakePod("pod0", testNS).OwnerReference(testSTS, stsGVK).
		ManagedByKueueLabel().Label(leaderworkersetv1.SetNameLabelKey, testLWS).
		Label(leaderworkersetv1.GroupIndexLabelKey, "0").NodeName("node").StatusPhase(corev1.PodPending).Obj()
	foreignPod := testingjobspod.MakePod("ds-pod", testNS).OwnerReference(testSTS, stsGVK).
		Label(leaderworkersetv1.SetNameLabelKey, testLWS).
		SuspendedByParent(disaggregatedset.FrameworkName).KueueSchedulingGate().TopologySchedulingGate().Obj()

	cases := map[string]struct {
		featureGates            map[featuregate.Feature]bool
		labelKeysToCopy         sets.Set[string]
		annotationsToCopy       sets.Set[string]
		onlyManageQueuedJobs    bool
		leaderWorkerSet         *leaderworkersetv1.LeaderWorkerSet
		statefulSets            []appsv1.StatefulSet
		workloads               []kueue.Workload
		pods                    []corev1.Pod
		workloadPriorityClasses []kueue.WorkloadPriorityClass
		// staleWorkloadCache makes the reconciler list no Workloads, as a cache
		// that has not seen an earlier create would.
		staleWorkloadCache   bool
		wantLeaderWorkerSets []leaderworkersetv1.LeaderWorkerSet
		wantWorkloads        []kueue.Workload
		wantPods             []corev1.Pod
		wantEvents           []utiltesting.EventRecord
		wantErr              error
	}{
		"keeps a reservation when the leader StatefulSet still uses the old template after the generation is observed": {
			leaderWorkerSet:      observedLWS.DeepCopy(),
			statefulSets:         []appsv1.StatefulSet{*leaderBeforeRollout},
			workloads:            []kueue.Workload{*reservedGroup0, *reservedGroup1},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*observedLWS},
			wantWorkloads:        []kueue.Workload{*reservedGroup0, *reservedGroup1},
		},
		"refreshes only the changed PodSet topology of an unreserved group": {
			leaderWorkerSet:      unreservedLWS.DeepCopy(),
			workloads:            []kueue.Workload{*unreservedGroup},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*unreservedLWS},
			wantWorkloads:        []kueue.Workload{*refreshedGroup},
		},
		"keeps an unreserved group's topology below the effective rollout partition": {
			leaderWorkerSet:      unreservedLWS.DeepCopy(),
			statefulSets:         []appsv1.StatefulSet{*rollingLeader},
			workloads:            []kueue.Workload{*unreservedGroup},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*unreservedLWS},
			wantWorkloads:        []kueue.Workload{*unreservedGroup},
		},
		"keeps an unreserved group's topology while a Pod is active": {
			leaderWorkerSet:      unreservedLWS.DeepCopy(),
			statefulSets:         []appsv1.StatefulSet{*statefulset.MakeStatefulSet(testSTS, testNS).Label(leaderworkersetv1.SetNameLabelKey, testLWS).Obj()},
			pods:                 []corev1.Pod{*boundPendingPod},
			workloads:            []kueue.Workload{*unreservedGroup},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*unreservedLWS},
			wantWorkloads:        []kueue.Workload{*unreservedGroup},
			wantPods:             []corev1.Pod{*boundPendingPod},
		},
		"replaces the stale reservation of an inactive group independently of a sibling with a bound Pending pod": {
			leaderWorkerSet:      updatedTopologyLWS.DeepCopy(),
			statefulSets:         []appsv1.StatefulSet{*statefulset.MakeStatefulSet(testSTS, testNS).Label(leaderworkersetv1.SetNameLabelKey, testLWS).Obj()},
			pods:                 []corev1.Pod{*boundPendingPod},
			workloads:            []kueue.Workload{*reservedGroup0, *reservedGroup1},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*updatedTopologyLWS},
			wantWorkloads:        []kueue.Workload{*reservedGroup0},
			wantPods:             []corev1.Pod{*boundPendingPod},
			wantEvents:           []utiltesting.EventRecord{deletedGroup1Event},
		},
		"keeps an inactive group's reservation when topology aware scheduling is disabled": {
			featureGates:         map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			leaderWorkerSet:      updatedTopologyLWS.DeepCopy(),
			statefulSets:         []appsv1.StatefulSet{*statefulset.MakeStatefulSet(testSTS, testNS).Label(leaderworkersetv1.SetNameLabelKey, testLWS).Obj()},
			pods:                 []corev1.Pod{*boundPendingPod},
			workloads:            []kueue.Workload{*reservedGroup0, *reservedGroup1},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*updatedTopologyLWS},
			wantWorkloads:        []kueue.Workload{*reservedGroup0, *reservedGroup1},
			wantPods:             []corev1.Pod{*boundPendingPod},
		},
		"keeps the reserved topology for a group below the rolling update partition": {
			leaderWorkerSet:      partitionedLWS.DeepCopy(),
			workloads:            []kueue.Workload{*reservedGroup0, *reservedGroup1},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*partitionedLWS},
			wantWorkloads:        []kueue.Workload{*reservedGroup0},
			wantEvents:           []utiltesting.EventRecord{deletedGroup1Event},
		},
		"keeps the reserved topology below the leader StatefulSet's effective partition": {
			leaderWorkerSet:      updatedTopologyLWS.DeepCopy(),
			statefulSets:         []appsv1.StatefulSet{*rollingLeader},
			workloads:            []kueue.Workload{*reservedGroup0, *reservedGroup1},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*updatedTopologyLWS},
			wantWorkloads:        []kueue.Workload{*reservedGroup0},
			wantEvents:           []utiltesting.EventRecord{deletedGroup1Event},
		},
		"keeps the reservation until the LeaderWorkerSet controller observes the template change": {
			leaderWorkerSet:      unobservedLWS.DeepCopy(),
			statefulSets:         []appsv1.StatefulSet{*leaderBeforeRollout},
			workloads:            []kueue.Workload{*reservedGroup0},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{*unobservedLWS},
			wantWorkloads:        []kueue.Workload{*reservedGroup0},
		},
		"should ignore a LeaderWorkerSet without a queue when only queued jobs are managed": {
			onlyManageQueuedJobs: true,
			leaderWorkerSet:      leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
		},
		"should create prebuilt workload": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should copy configured labels into the created workload": {
			labelKeysToCopy: sets.New("toCopyKey"),
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Label("toCopyKey", "toCopyValue").
				Label("doNotCopyKey", "doNotCopyValue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Label("toCopyKey", "toCopyValue").
					Label("doNotCopyKey", "doNotCopyValue").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Label("toCopyKey", "toCopyValue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should copy configured annotations into the created workload when CustomMetricLabels is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.CustomMetricLabels: false,
			},
			annotationsToCopy: sets.New("toCopyAnnotation"),
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Annotation("toCopyAnnotation", "toCopyValue").
				Annotation("doNotCopyAnnotation", "doNotCopyValue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation("toCopyAnnotation", "toCopyValue").
					Annotation("doNotCopyAnnotation", "doNotCopyValue").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation("toCopyAnnotation", "toCopyValue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should not fail when the workload cache has not seen an earlier create": {
			leaderWorkerSet:    leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			staleWorkloadCache: true,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Obj(),
			},
		},
		"should create prebuilt workload with leader template": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workloads with leader template": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Replicas(2).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Replicas(2).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj(),
					).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "1"),
					),
				},
			},
		},
		"should create prebuilt workload with custom annotations propagated to podsets": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Replicas(1).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Annotations: map[string]string{
						"custom-leader-annotation":                  "leader-value",
						"leaderworkerset.sigs.k8s.io/template-hash": "12345",
					},
					Labels: map[string]string{
						"leaderworkerset.sigs.k8s.io/name":        testLWS,
						"leaderworkerset.sigs.k8s.io/group-index": "1",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Annotations: map[string]string{
						"custom-worker-annotation":                  "worker-value",
						"leaderworkerset.sigs.k8s.io/template-hash": "12345",
					},
					Labels: map[string]string{
						"leaderworkerset.sigs.k8s.io/name":        testLWS,
						"leaderworkerset.sigs.k8s.io/group-index": "1",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Replicas(1).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Annotations: map[string]string{
							"custom-leader-annotation":                  "leader-value",
							"leaderworkerset.sigs.k8s.io/template-hash": "12345",
						},
						Labels: map[string]string{
							"leaderworkerset.sigs.k8s.io/name":        testLWS,
							"leaderworkerset.sigs.k8s.io/group-index": "1",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					WorkerTemplate(corev1.PodTemplateSpec{
						Annotations: map[string]string{
							"custom-worker-annotation":                  "worker-value",
							"leaderworkerset.sigs.k8s.io/template-hash": "12345",
						},
						Labels: map[string]string{
							"leaderworkerset.sigs.k8s.io/name":        testLWS,
							"leaderworkerset.sigs.k8s.io/group-index": "1",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							Annotations(map[string]string{"custom-leader-annotation": "leader-value"}).
							Labels(map[string]string{"leaderworkerset.sigs.k8s.io/name": testLWS}).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							Annotations(map[string]string{"custom-worker-annotation": "worker-value"}).
							Labels(map[string]string{"leaderworkerset.sigs.k8s.io/name": testLWS}).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with required topology annotation": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Annotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Annotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					WorkerTemplate(corev1.PodTemplateSpec{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							RequiredTopologyRequest("cloud.com/block").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							RequiredTopologyRequest("cloud.com/block").
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload without topology request if TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Annotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Annotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					WorkerTemplate(corev1.PodTemplateSpec{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with workload priority": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high-priority").
				UID(testLWS).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("high-priority").PriorityValue(5000).Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("high-priority").
					UID(testLWS).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					WorkloadPriorityClassRef("high-priority").
					Priority(5000).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		// Two replicas, and each component resolves the class for itself, so the
		// warning arrives once per component. The fix for #13820 collapses those
		// into a single resolution, and the event follows it.
		"should report a missing workload priority class when creating the workloads": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("missing-wpc").
				Replicas(2).
				UID(testLWS).
				Obj(),
			// missing-wpc is deliberately not created.
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("missing-wpc").
					Replicas(2).
					UID(testLWS).
					Obj(),
			},
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		// The label is changed on a set that already has Workloads, so the class
		// is resolved once for the whole set and the warning arrives once. Neither
		// Workload is rewritten.
		"should report a missing workload priority class when the label is changed": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("missing-wpc").
				Replicas(2).
				UID(testLWS).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("existing-wpc").PriorityValue(1000).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("missing-wpc").
					Replicas(2).
					UID(testLWS).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
			},
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		// Workload 0 already names high-wpc but holds an old value of it. When
		// workload 1 moves to the class, workload 0 gets the current value too.
		"should give every workload the same priority when the label is changed": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high-wpc").
				Replicas(2).
				UID(testLWS).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("low-wpc").PriorityValue(100).Obj(),
				*utiltestingapi.MakeWorkloadPriorityClass("high-wpc").PriorityValue(1000).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("high-wpc").
					Priority(500).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("low-wpc").
					Priority(100).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("high-wpc").
					Replicas(2).
					UID(testLWS).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("high-wpc").
					Priority(1000).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("high-wpc").
					Priority(1000).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   fmt.Sprintf("Updated workload priority class: %s/%s", testNS, GetWorkloadName(testLWS, testLWS, "0")),
				},
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   fmt.Sprintf("Updated workload priority class: %s/%s", testNS, GetWorkloadName(testLWS, testLWS, "1")),
				},
			},
		},
		"should update the workload priority when the queue update fails": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				WorkloadPriorityClass("high-wpc").
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("low-wpc").PriorityValue(100).Obj(),
				*utiltestingapi.MakeWorkloadPriorityClass("high-wpc").PriorityValue(1000).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("low-wpc").
					Priority(100).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					WorkloadPriorityClass("high-wpc").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("high-wpc").
					Priority(1000).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantErr: workloadUpdateErr,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   fmt.Sprintf("Updated workload priority class: %s/%s", testNS, GetWorkloadName(testLWS, testLWS, "0")),
				},
			},
		},
		"should update the workload queue when the workload priority class is missing": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				WorkloadPriorityClass("missing-wpc").
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("existing-wpc").PriorityValue(1000).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					WorkloadPriorityClass("missing-wpc").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
			},
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		"should update prebuilt workload if queue-name label is set": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantEvents: nil,
		},
		"should update tolerations on workload without quota reservation": {
			featureGates: map[featuregate.Feature]bool{features.AllowAddingPodTemplateTolerations: true},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				LeaderTemplateSpecToleration(corev1.Toleration{Key: "old", Operator: corev1.TolerationOpExists}).
				LeaderTemplateSpecToleration(corev1.Toleration{Key: "leader", Operator: corev1.TolerationOpExists}).
				WorkerTemplateSpecToleration(corev1.Toleration{Key: "worker", Operator: corev1.TolerationOpExists}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					LeaderTemplate(corev1.PodTemplateSpec{}).
					LeaderTemplateSpecToleration(corev1.Toleration{Key: "old", Operator: corev1.TolerationOpExists}).
					LeaderTemplateSpecToleration(corev1.Toleration{Key: "leader", Operator: corev1.TolerationOpExists}).
					WorkerTemplateSpecToleration(corev1.Toleration{Key: "worker", Operator: corev1.TolerationOpExists}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							Toleration(corev1.Toleration{Key: "old", Operator: corev1.TolerationOpExists}).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 1).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							Toleration(corev1.Toleration{Key: "old", Operator: corev1.TolerationOpExists}).
							Toleration(corev1.Toleration{Key: "leader", Operator: corev1.TolerationOpExists}).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 1).
							Toleration(corev1.Toleration{Key: "worker", Operator: corev1.TolerationOpExists}).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
		},
		"should update tolerations on workload with quota reservation": {
			featureGates: map[featuregate.Feature]bool{features.AllowAddingPodTemplateTolerations: true},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				WorkerTemplateSpecToleration(corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					WorkerTemplateSpecToleration(corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Toleration(corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}).
						Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"should not update tolerations on workload when the feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{features.AllowAddingPodTemplateTolerations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				WorkerTemplateSpecToleration(corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					WorkerTemplateSpecToleration(corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should not update tolerations on workload of a MultiKueue remote leaderworkerset": {
			featureGates: map[featuregate.Feature]bool{features.AllowAddingPodTemplateTolerations: true},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Label(kueue.MultiKueueOriginLabel, "origin").
				WorkerTemplateSpecToleration(corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Label(kueue.MultiKueueOriginLabel, "origin").
					WorkerTemplateSpecToleration(corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should update prebuilt workload if queue-name label is updated": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantEvents: nil,
		},
		"should atomically update prebuilt workload queue name and AdmissionGatedBy": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadIdentifierAnnotations: false,
				features.AdmissionGatedBy:              true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   `Updated workload AdmissionGatedBy to "example.com/controller1"`,
				},
			},
		},
		"should not persist workload metadata when the combined update fails": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadIdentifierAnnotations: false,
				features.AdmissionGatedBy:              true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantErr: workloadUpdateErr,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		// Simulates a scale-down (spec.Replicas=1) while status.Replicas=2 hasn't caught up yet.
		// During scale-down, the LWS controller updates status.Replicas asynchronously, so
		// there is a window where status.Replicas > spec.Replicas. Without isRollingUpdateWithSurge,
		// maxSurge > 0 alone would cause filterWorkloads to use stale status.Replicas, keeping the
		// excess workload in toUpdate instead of toDelete.
		"should delete excess workload on scale down with maxSurge configured but no active rolling update": {
			leaderWorkerSet: func() *leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(1).Obj()
				lws.Status.Replicas = 2
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
			}(),
			wantLeaderWorkerSets: func() []leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(1).Obj()
				lws.Status.Replicas = 2
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return []leaderworkersetv1.LeaderWorkerSet{*lws}
			}(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		// During an active rolling update (UpdatedReplicas < spec.Replicas), surge workloads
		// must be kept. This, paired with the test above, proves that maxSurge > 0 alone is
		// not sufficient — the UpdatedReplicas check is needed to distinguish the two cases.
		"should keep surge workloads during active rolling update with maxSurge": {
			leaderWorkerSet: func() *leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
			}(),
			wantLeaderWorkerSets: func() []leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return []leaderworkersetv1.LeaderWorkerSet{*lws}
			}(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		// LeaderWorkerSet documents maxSurge as an absolute number or a percentage of the
		// replicas at the start of the update, so the case above has to hold in both forms.
		"should keep surge workloads during active rolling update with a percentage maxSurge": {
			leaderWorkerSet: func() *leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromString("50%"),
				}
				return lws
			}(),
			wantLeaderWorkerSets: func() []leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromString("50%"),
				}
				return []leaderworkersetv1.LeaderWorkerSet{*lws}
			}(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should delete LeaderWorkerSet ownerReference from the redundant prebuilt workload": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod2", "test-pod2-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should create prebuilt workload with AdmissionGatedBy annotation": {
			featureGates: map[featuregate.Feature]bool{
				features.AdmissionGatedBy: true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with multiple AdmissionGatedBy gates": {
			featureGates: map[featuregate.Feature]bool{
				features.AdmissionGatedBy: true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should not propagate AdmissionGatedBy annotation when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.AdmissionGatedBy: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should not propagate WaitForPodsReadyAnnotation annotation to an existing workload nor emit an event when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadLevelWaitForPodsReady: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Annotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should not propagate WaitForPodsReadyAnnotation annotation when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadLevelWaitForPodsReady: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Annotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should backfill queue-name on an already adopted pod": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			// Migration case: the pod was already adopted but carries a stale queue name,
			// because it came from a template the webhook never stamped.
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					Queue("old-queue").
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueSchedulingGate().
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					Queue("queue").
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueSchedulingGate().
					KueueFinalizer().
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should not backfill queue-name on an ungated pod": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			// The pod is already ungated, so the pod webhook rejects a queue-name
			// change as immutable. Leave it alone: it is running and does not need it.
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueFinalizer().
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should set queue-name when adopting a pod without it": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			// Migration case: unlike the test above, this pod comes from a template the
			// webhook never stamped, so the reconciler sets the queue name while adopting
			// it instead of leaving the pod unlabelled for a later reconcile to backfill.
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					KueueSchedulingGate().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					Queue("queue").
					GroupNameAnnotation(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadAnnotation(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueSchedulingGate().
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should keep gates for a Pod suspended by another framework after LeaderWorkerSet deletion": {
			pods:     []corev1.Pod{*foreignPod.DeepCopy()},
			wantPods: []corev1.Pod{*foreignPod.DeepCopy()},
		},
		"should ungate pods if leaderworkerset is deleted": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: nil,
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: nil,
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: nil,
			wantWorkloads:        nil,
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
		},
		"should ungate pods if statefulSet is deleted": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets:    nil,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
		},
		"should keep gates on current revision pods during a statefulSet rollout": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					CurrentRevision("revision-1").
					UpdateRevision("revision-2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Label(appsv1.ControllerRevisionHashLabelKey, "revision-1").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Label(appsv1.ControllerRevisionHashLabelKey, "revision-1").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
		},
		"should ignore succeeded pod": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					StatusPhase(corev1.PodSucceeded).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					StatusPhase(corev1.PodSucceeded).
					KueueFinalizer().
					Obj(),
			},
		},
		"should ignore failed pod": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					KueueFinalizer().
					Obj(),
			},
		},
		"should ignore deleted pod": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					DeletionTimestamp(now).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					DeletionTimestamp(now).
					KueueFinalizer().
					Obj(),
			},
		},
		"shouldn't set default values with managed-by-kueue label": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					ManagedByKueueLabel().
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					ManagedByKueueLabel().
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
		},
		"shouldn't set default values without group index label": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
		},
		"should set default values even if queue-name changed and workload has quota reservation": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Queue("queue").Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			// Normal case: the webhook stamped the queue name on the pod template, so the
			// pod already carries it when the reconciler adopts it.
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Queue("queue").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Queue("queue").Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					Queue("queue").
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Leader pod doesn't have a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (worker template, leader pod)": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Worker pod has a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (worker template, worker pod)": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Leader pod doesn't have a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (leader+worker template, leader pod)": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     utiltestingjobs.TestDefaultContainerImage,
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:      "c",
									Image:     utiltestingjobs.TestDefaultContainerImage,
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, leaderPodSetName).
					Obj(),
			},
		},
		// Worker pod has a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (leader+worker template, worker pod)": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     utiltestingjobs.TestDefaultContainerImage,
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:      "c",
									Image:     utiltestingjobs.TestDefaultContainerImage,
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Annotation(podconstants.RoleHashAnnotation, workerPodSetName).
					Obj(),
			},
		},
		"should set default values using origin UID in MultiKueue scenario": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
				Label(kueue.MultiKueueOriginLabel, "origin1").
				Annotation(kueue.MultiKueueOriginUIDAnnotation, "origin-uid").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("origin-uid", testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "origin-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("origin-uid", testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName("origin-uid", testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName("origin-uid", testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)
			staleWorkloadCache := tc.staleWorkloadCache
			clientBuilder = clientBuilder.WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*kueue.WorkloadList); ok && staleWorkloadCache {
						return nil
					}
					return c.List(ctx, list, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					// Refuse a queueName change while quota is reserved, as the Workload CEL rule does.
					if wl, ok := obj.(*kueue.Workload); ok && errors.Is(tc.wantErr, workloadUpdateErr) {
						stored := &kueue.Workload{}
						if err := c.Get(ctx, client.ObjectKeyFromObject(wl), stored); err != nil {
							return err
						}
						if workload.HasQuotaReservation(stored) && stored.Spec.QueueName != wl.Spec.QueueName {
							return workloadUpdateErr
						}
					}
					return c.Update(ctx, obj, opts...)
				},
			})
			indexer := utiltesting.AsIndexer(clientBuilder)

			objs := make([]client.Object, 0, len(tc.workloads)+len(tc.workloadPriorityClasses)+3)
			objs = append(objs, oldRevision.DeepCopy(), updatedRevision.DeepCopy())
			if tc.leaderWorkerSet != nil {
				objs = append(objs, tc.leaderWorkerSet)
			}
			for _, sts := range tc.statefulSets {
				objs = append(objs, &sts)
			}
			for _, wl := range tc.workloads {
				objs = append(objs, &wl)
			}
			for _, pod := range tc.pods {
				objs = append(objs, &pod)
			}
			for _, wpc := range tc.workloadPriorityClasses {
				objs = append(objs, &wpc)
			}

			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := &utiltesting.EventRecorder{}

			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder,
				jobframework.WithLabelKeysToCopy(tc.labelKeysToCopy),
				jobframework.WithAnnotationsToCopy(tc.annotationsToCopy),
				jobframework.WithManageJobsWithoutQueueName(!tc.onlyManageQueuedJobs),
			)
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			_, err = reconciler.Reconcile(ctx, request)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}
			// The checks below read the stored Workloads.
			staleWorkloadCache = false

			gotLeaderWorkerSets := &leaderworkersetv1.LeaderWorkerSetList{}
			if err := kClient.List(ctx, gotLeaderWorkerSets, client.InNamespace(request.Namespace)); err != nil {
				t.Fatalf("Could not list LeaderWorkerSets after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantLeaderWorkerSets, gotLeaderWorkerSets.Items, baseCmpOpts...); diff != "" {
				t.Errorf("LeaderWorkerSets after reconcile (-want,+got):\n%s", diff)
			}

			gotWorkloads := kueue.WorkloadList{}
			err = kClient.List(ctx, &gotWorkloads, client.InNamespace(request.Namespace))
			if err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			gotPods := corev1.PodList{}
			err = kClient.List(ctx, &gotPods, client.InNamespace(request.Namespace))
			if err != nil {
				t.Fatalf("Could not get Pods after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantPods, gotPods.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestDeleteWorkloadPreservesConcurrentAdmission(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj()
	wl := utiltestingapi.MakeWorkload(GetWorkloadName(lws.UID, lws.Name, "0"), lws.Namespace).
		UID("workload-uid").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now()).Obj()
	c := utiltesting.NewClientBuilder().WithObjects(wl).WithStatusSubresource(wl).Build()
	stale := &kueue.Workload{}
	key := client.ObjectKeyFromObject(wl)
	if err := c.Get(ctx, key, stale); err != nil {
		t.Fatalf("Get Workload: %v", err)
	}
	admitted := (&utiltestingapi.WorkloadWrapper{Workload: *stale.DeepCopy()}).AdmittedAt(true, time.Now()).Obj()
	if err := c.Status().Update(ctx, admitted); err != nil {
		t.Fatalf("Admit Workload: %v", err)
	}
	recorder := &utiltesting.EventRecorder{}
	r := &Reconciler{client: c, record: recorder}
	if err := r.deleteWorkloadWithStaleTopology(ctx, lws, stale); !apierrors.IsConflict(err) {
		t.Fatalf("Delete Workload got error %v, want conflict", err)
	}
	if err := c.Get(ctx, key, admitted); err != nil {
		t.Fatalf("Get Workload after delete conflict: %v", err)
	}
	if !workload.IsAdmitted(admitted) {
		t.Error("Workload admission was lost")
	}
	if len(recorder.RecordedEvents) != 0 {
		t.Errorf("Unexpected events after delete conflict: %v", recorder.RecordedEvents)
	}
}

func TestRefreshTopologyPreservesConcurrentQuotaReservation(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Queue("lq").Obj()
	wl := utiltestingapi.MakeWorkload(GetWorkloadName(lws.UID, lws.Name, "0"), lws.Namespace).
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).Obj()).Obj()
	c := utiltesting.NewClientBuilder().WithObjects(wl).WithStatusSubresource(wl).Build()
	stale := &kueue.Workload{}
	key := client.ObjectKeyFromObject(wl)
	if err := c.Get(ctx, key, stale); err != nil {
		t.Fatalf("Get Workload: %v", err)
	}
	reserved := (&utiltestingapi.WorkloadWrapper{Workload: *stale.DeepCopy()}).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now()).Obj()
	if err := c.Status().Update(ctx, reserved); err != nil {
		t.Fatalf("Reserve quota: %v", err)
	}
	r := &Reconciler{client: c}
	updates := []topologyRequestUpdate{{index: 0, request: &kueue.PodSetTopologyRequest{Required: new(corev1.LabelHostname)}}}
	if err := r.updateWorkload(ctx, lws, stale, updates); !apierrors.IsConflict(err) {
		t.Fatalf("Refresh topology got error %v, want conflict", err)
	}
	got := &kueue.Workload{}
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("Get Workload after conflict: %v", err)
	}
	if diff := cmp.Diff(reserved, got); diff != "" {
		t.Errorf("Workload after conflict (-want/+got):\n%s", diff)
	}
}

// TestReconcileWorkloadsDoesNotCancelTheOtherBranches pins that a failing
// branch leaves the others' context alone. The three hold disjoint sets of
// Workloads, and parallelize.Until checks for cancellation before each item,
// so under a derived context a delete that failed first could stop the create
// and update branches before they ran at all.
func TestReconcileWorkloadsDoesNotCancelTheOtherBranches(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		UID(testLWS).
		Replicas(1).
		Queue("lq").
		WorkloadPriorityClass("wpc").
		Obj()
	// Not among the names replicas=1 asks for, so the delete branch takes it.
	surplus := utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "7"), testNS).
		JobUID(testLWS).
		OwnerReference(gvk, testLWS, testLWS).
		Obj()
	wpc := utiltestingapi.MakeWorkloadPriorityClass("wpc").PriorityValue(100).Obj()

	var (
		createReached = make(chan struct{})
		deleteFailed  = make(chan struct{})
		reachedOnce   sync.Once
		failedOnce    sync.Once
		cancelledHere atomic.Bool
		errNotOrdered = errors.New("the delete branch never failed")
		errDeleting   = errors.New("deleting the surplus workload")
	)

	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
		// The class lookup stands in for the create branch: it announces that
		// the branch is inside a work item, then waits for the delete to fail,
		// so a cancellation would have landed by the time it looks.
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isClass := obj.(*kueue.WorkloadPriorityClass); isClass {
				reachedOnce.Do(func() { close(createReached) })
				if !utiltesting.AwaitBranch(deleteFailed) {
					return errNotOrdered
				}
				if utiltesting.ObserveCancellation(ctx) {
					cancelledHere.Store(true)
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, isWorkload := obj.(*kueue.Workload); isWorkload {
				if !utiltesting.AwaitBranch(createReached) {
					return errNotOrdered
				}
				failedOnce.Do(func() { close(deleteFailed) })
				return errDeleting
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(lws, surplus, wpc).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	_, err = reconciler.Reconcile(ctx, reconcile.Request{Name: testLWS, Namespace: testNS})
	if errors.Is(err, errNotOrdered) {
		t.Fatalf("Reconcile() error = %v, so the branches never interleaved and the ordering below was not exercised", err)
	}
	if !errors.Is(err, errDeleting) {
		t.Fatalf("Reconcile() error = %v, want %v", err, errDeleting)
	}
	if cancelledHere.Load() {
		t.Error("the create branch ran under a context the delete failure had cancelled")
	}
	// An uncancelled context is only half of it: the branch also has to have finished its work.
	created := &kueue.Workload{}
	if err := kClient.Get(ctx, types.NamespacedName{Name: GetWorkloadName(testLWS, testLWS, "0"), Namespace: testNS}, created); err != nil {
		t.Fatalf("Getting the Workload the create branch was to make: %v", err)
	}
	if created.Spec.Priority == nil || *created.Spec.Priority != 100 {
		t.Errorf("created Workload priority = %v, want the class value 100", created.Spec.Priority)
	}
}

func TestIsRollingUpdateWithSurge(t *testing.T) {
	cases := map[string]struct {
		maxSurge intstr.IntOrString
		want     bool
		wantErr  error
	}{
		"absolute":        {maxSurge: intstr.FromInt32(1), want: true},
		"absolute zero":   {maxSurge: intstr.FromInt32(0)},
		"percentage":      {maxSurge: intstr.FromString("30%"), want: true},
		"percentage all":  {maxSurge: intstr.FromString("100%"), want: true},
		"percentage zero": {maxSurge: intstr.FromString("0%")},
		"quoted number":   {maxSurge: intstr.FromString("2"), wantErr: errInvalidMaxSurge},
		"neither":         {maxSurge: intstr.FromString("some"), wantErr: errInvalidMaxSurge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lws := &leaderworkersetv1.LeaderWorkerSet{}
			lws.Spec.Replicas = new(int32(4))
			lws.Status.UpdatedReplicas = 1
			lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{MaxSurge: tc.maxSurge}
			got, err := isRollingUpdateWithSurge(lws)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("isRollingUpdateWithSurge() error (-want,+got):\n%s", diff)
			}
			if got != tc.want {
				t.Errorf("isRollingUpdateWithSurge() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSyncWorkload(t *testing.T) {
	baseWl := utiltestingapi.MakeWorkload("wl", testNS).
		Queue("queue").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj())
	toleration := corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists}

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		lws          *leaderworkersetv1.LeaderWorkerSet
		wl           *kueue.Workload
		wantResult   workloadSyncResult
		wantWl       *kueue.Workload
	}{
		"no changes": {
			lws:    leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").Obj(),
			wl:     baseWl.Clone().Obj(),
			wantWl: baseWl.Clone().Obj(),
		},
		"queue name changed": {
			lws:        leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("new-queue").Obj(),
			wl:         baseWl.Clone().Obj(),
			wantResult: workloadSyncResult{updated: true, queueNameUpdated: true},
			wantWl:     baseWl.Clone().Queue("new-queue").Obj(),
		},
		"tolerations added when feature gate is enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.AllowAddingPodTemplateTolerations: true,
			},
			lws:        leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").WorkerTemplateSpecToleration(toleration).Obj(),
			wl:         baseWl.Clone().Obj(),
			wantResult: workloadSyncResult{updated: true, tolerationsUpdated: true},
			wantWl: baseWl.Clone().
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Toleration(toleration).Obj()).
				Obj(),
		},
		"tolerations not synced when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.AllowAddingPodTemplateTolerations: false,
			},
			lws:    leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").WorkerTemplateSpecToleration(toleration).Obj(),
			wl:     baseWl.Clone().Obj(),
			wantWl: baseWl.Clone().Obj(),
		},
		"tolerations not synced on MultiKueue worker": {
			featureGates: map[featuregate.Feature]bool{
				features.AllowAddingPodTemplateTolerations: true,
			},
			lws:    leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").Label(kueue.MultiKueueOriginLabel, "origin").WorkerTemplateSpecToleration(toleration).Obj(),
			wl:     baseWl.Clone().Obj(),
			wantWl: baseWl.Clone().Obj(),
		},
		"AdmissionGatedBy annotation changed when feature gate is enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.AdmissionGatedBy: true,
			},
			lws:        leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate").Obj(),
			wl:         baseWl.Clone().Obj(),
			wantResult: workloadSyncResult{updated: true, admissionGatedByUpdated: true},
			wantWl:     baseWl.Clone().Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate").Obj(),
		},
		"AdmissionGatedBy annotation not propagated when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.AdmissionGatedBy: false,
			},
			lws:    leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/gate").Obj(),
			wl:     baseWl.Clone().Obj(),
			wantWl: baseWl.Clone().Obj(),
		},
		"WaitForPodsReady annotation synced when feature gate is enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadLevelWaitForPodsReady: true,
			},
			lws:        leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").Annotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":30}`).Obj(),
			wl:         baseWl.Clone().Obj(),
			wantResult: workloadSyncResult{updated: true, waitForPodsReadyEvent: jobframework.WaitForPodsReadyEventUpdated},
			wantWl:     baseWl.Clone().WaitForPodsReady(&kueue.WaitForPodsReady{TimeoutSeconds: new(int64(30))}).Obj(),
		},
		"WaitForPodsReady annotation not synced when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadLevelWaitForPodsReady: false,
			},
			lws:    leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).Queue("queue").Annotation(constants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":30}`).Obj(),
			wl:     baseWl.Clone().Obj(),
			wantWl: baseWl.Clone().Obj(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)

			lws := tc.lws.DeepCopy()
			wl := tc.wl.DeepCopy()
			got := syncWorkload(ctx, lws, wl)
			if diff := cmp.Diff(tc.wantResult, got, cmp.AllowUnexported(workloadSyncResult{})); diff != "" {
				t.Errorf("syncWorkload() result (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWl, wl, baseCmpOpts...); diff != "" {
				t.Errorf("workload after syncWorkload() (-want,+got):\n%s", diff)
			}
		})
	}
}
