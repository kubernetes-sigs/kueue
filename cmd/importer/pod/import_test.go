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

package pod

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/cache"
	"sigs.k8s.io/kueue/cmd/importer/mapping"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	controllerpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestImportNamespace(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	basePodWrapper := testingpod.MakePod("pod", testingNamespace).
		UID("pod").
		Label(testingQueueLabel, "q1").
		Image("img", nil).
		Request(corev1.ResourceCPU, "1")

	baseWlWrapper := utiltestingapi.MakeWorkload("pod-pod-b17ab", testingNamespace).
		ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "pod").
		Label(controllerconstants.JobUIDLabel, "pod").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Queue("lq1").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Image("img").
			Request(corev1.ResourceCPU, "1").
			PodIndexLabel(new(kueue.PodGroupPodIndexLabel)).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq1").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "f1", "1").
				Obj()).
			Obj(), now).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: "Imported into ClusterQueue cq1",
		}).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadAdmitted,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: "Imported into ClusterQueue cq1",
		})

	baseLocalQueue := utiltestingapi.MakeLocalQueue("lq1", testingNamespace).ClusterQueue("cq1")
	baseClusterQueue := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "1", "0").Obj())

	baseGpuPodWrapper := testingpod.MakePod("pod-gpu", testingNamespace).
		UID("pod-gpu").
		Label(testingQueueLabel, "q1").
		Image("img", nil).
		Request(corev1.ResourceCPU, "1").
		Request(corev1.ResourceName("nvidia.com/gpu"), "1")
	baseGpuManagedPodWrapper := baseGpuPodWrapper.Clone().
		Label(controllerconstants.QueueLabel, "lq1").
		ManagedByKueueLabel()

	baseGpuWlWrapper := utiltestingapi.MakeWorkload(controllerpod.GetWorkloadNameForPod("pod-gpu", types.UID("pod-gpu")), testingNamespace).
		ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod-gpu", "pod-gpu").
		Label(controllerconstants.JobUIDLabel, "pod-gpu").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Queue("lq1").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Image("img").
			Request(corev1.ResourceCPU, "1").
			Request(corev1.ResourceName("nvidia.com/gpu"), "1").
			PodIndexLabel(ptr.To(kueue.PodGroupPodIndexLabel)).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq1").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "cpu-flavor", "1").
				Assignment(corev1.ResourceName("nvidia.com/gpu"), "gpu-flavor", "1").
				Obj()).
			Obj(), now).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: "Imported into ClusterQueue cq1",
		}).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadAdmitted,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: "Imported into ClusterQueue cq1",
		})

	cpuOnlyClusterQueue :=
		*utiltestingapi.MakeClusterQueue("cq1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("cpu-flavor").
					Resource(corev1.ResourceCPU, "10", "0").
					Obj(),
			)

	cpuAndGpuClusterQueue :=
		cpuOnlyClusterQueue.Clone().ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("gpu-flavor").
				Resource(corev1.ResourceName("nvidia.com/gpu"), "10", "0").
				Obj(),
		)

	podCmpOpts := cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}

	wlCmpOpts := cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime"),
	}

	baseMapping := mapping.Rules{{
		Match:        mapping.Match{Labels: map[string]string{testingQueueLabel: "q1"}},
		ToLocalQueue: "lq1",
	}}

	cases := map[string]struct {
		pods            []corev1.Pod
		clusterQueue    kueue.ClusterQueue
		localQueue      kueue.LocalQueue
		addLabels       map[string]string
		flavors         []kueue.ResourceFlavor
		priorityClasses []schedulingv1.PriorityClass
		wantPods        []corev1.Pod
		wantWorkloads   []kueue.Workload
		wantError       error
	}{
		"create one": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "lq1").
					ManagedByKueueLabel().
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWlWrapper.DeepCopy(),
			},
		},
		"create one, add labels": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			addLabels: map[string]string{
				"new.lbl": "val",
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "lq1").
					ManagedByKueueLabel().
					Label("new.lbl", "val").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWlWrapper.Clone().
					Label("new.lbl", "val").
					Obj(),
			},
		},
		"pod already carries matching queue label but is missing managed-by and add-labels": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "lq1").
					Obj(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			addLabels: map[string]string{
				"new.lbl": "val",
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "lq1").
					ManagedByKueueLabel().
					Label("new.lbl", "val").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWlWrapper.Clone().
					Label("new.lbl", "val").
					Obj(),
			},
		},
		"create one, add labels visible during workload construction": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			addLabels: map[string]string{
				controllerconstants.MaxExecTimeSecondsLabel: "3600",
				controllerconstants.PrebuiltWorkloadLabel:   "prebuilt-wl",
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "lq1").
					ManagedByKueueLabel().
					Label(controllerconstants.MaxExecTimeSecondsLabel, "3600").
					Label(controllerconstants.PrebuiltWorkloadLabel, "prebuilt-wl").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWlWrapper.Clone().
					Name("prebuilt-wl").
					Label(controllerconstants.MaxExecTimeSecondsLabel, "3600").
					Label(controllerconstants.PrebuiltWorkloadLabel, "prebuilt-wl").
					MaximumExecutionTimeSeconds(3600).
					Obj(),
			},
		},
		"pod has conflicting pre-existing queue label": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "other-lq").
					Obj(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "other-lq").
					Obj(),
			},
			wantError:     &queueLabelConflictError{CurrentQueue: "other-lq", ExpectedQueue: "lq1"},
			wantWorkloads: []kueue.Workload{},
		},
		"missing cluster queue": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: kueue.ClusterQueue{}, // use a zero-value ClusterQueue; LocalQueue still points to cq1, so lookup triggers ErrCQNotFound
			wantPods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			wantError: cache.ErrCQNotFound,
		},
		"imports a pod requesting cpu and gpu and assigns each resource to its matching resource-group flavor": {
			pods: []corev1.Pod{
				*baseGpuPodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *cpuAndGpuClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("cpu-flavor").Obj(),
				*utiltestingapi.MakeResourceFlavor("gpu-flavor").Obj(),
			},
			wantPods: []corev1.Pod{
				*baseGpuManagedPodWrapper.DeepCopy(),
			},
			wantWorkloads: []kueue.Workload{
				*baseGpuWlWrapper.DeepCopy(),
			},
		},
		"returns an error without mutating pod or creating workload when a requested resource is not covered by the cluster queue": {
			pods: []corev1.Pod{
				*baseGpuPodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *cpuOnlyClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("cpu-flavor").Obj(),
			},
			wantError: &resourceNotCoveredError{Resource: corev1.ResourceName("nvidia.com/gpu"), ClusterQueue: "cq1"},
			wantPods: []corev1.Pod{
				*baseGpuPodWrapper.DeepCopy(),
			},
			wantWorkloads: []kueue.Workload{},
		},
		"returns an error without mutating pod or creating workload when cluster queue references a missing resource flavor": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors:      []kueue.ResourceFlavor{},
			wantError:    cache.ErrCQInvalid,
			wantPods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			wantWorkloads: []kueue.Workload{},
		},
		"returns an error without mutating pod or creating workload when cluster queue has no resource groups": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *utiltestingapi.MakeClusterQueue("cq1").Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			wantError: cache.ErrCQInvalid,
			wantPods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			wantWorkloads: []kueue.Workload{},
		},
		"imports a pod referencing a known priority class": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().PriorityClass("p-class").Obj(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			priorityClasses: []schedulingv1.PriorityClass{
				{ObjectMeta: metav1.ObjectMeta{Name: "p-class"}, Value: 100},
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().PriorityClass("p-class").
					Label(controllerconstants.QueueLabel, "lq1").
					ManagedByKueueLabel().
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWlWrapper.Clone().
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Image("img").
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kueue.PodGroupPodIndexLabel)).
						PriorityClass("p-class").
						Obj()).
					PodPriorityClassRef("p-class").
					Priority(100).
					Obj(),
			},
		},
		"returns an error without mutating pod or creating workload when priority class is unknown": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().PriorityClass("missing-class").Obj(),
			},
			localQueue:   *baseLocalQueue.Obj(),
			clusterQueue: *baseClusterQueue.Obj(),
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("f1").Obj(),
			},
			wantError: cache.ErrPCNotFound,
			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().PriorityClass("missing-class").Obj(),
			},
			wantWorkloads: []kueue.Workload{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			podsList := corev1.PodList{Items: tc.pods}
			cqList := kueue.ClusterQueueList{Items: []kueue.ClusterQueue{tc.clusterQueue}}
			lqList := kueue.LocalQueueList{Items: []kueue.LocalQueue{tc.localQueue}}
			rfList := kueue.ResourceFlavorList{Items: tc.flavors}
			pcList := schedulingv1.PriorityClassList{Items: tc.priorityClasses}

			builder := utiltesting.NewClientBuilder().
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).WithStatusSubresource(&kueue.Workload{}).
				WithLists(&podsList, &cqList, &lqList, &rfList, &pcList)

			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			mpc, err := cache.Load(ctx, client, []string{testingNamespace}, baseMapping, tc.addLabels, nil)
			if err != nil {
				t.Fatalf("Unexpected cache load error: %s", err)
			}

			gotErr := Import(ctx, client, mpc, 8)
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			err = client.List(ctx, &podsList)
			if err != nil {
				t.Errorf("Unexpected list pod error: %s", err)
			}
			if diff := cmp.Diff(tc.wantPods, podsList.Items, podCmpOpts...); diff != "" {
				t.Errorf("Unexpected pods (-want/+got)\n%s", diff)
			}

			wlList := kueue.WorkloadList{}
			err = client.List(ctx, &wlList)
			if err != nil {
				t.Errorf("Unexpected list workloads error: %s", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, wlList.Items, wlCmpOpts...); diff != "" {
				t.Errorf("Unexpected workloads (-want/+got)\n%s", diff)
			}
		})
	}
}
