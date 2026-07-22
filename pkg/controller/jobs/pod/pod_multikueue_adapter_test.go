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
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	basePodName := "test-pod"
	basePodBuilder := utiltestingpod.MakePod(basePodName, TestNamespace)

	groupSize := 3
	podGroup := basePodBuilder.
		Clone().
		MakePodGroupWrappers(groupSize)

	podGroupWithWl := basePodBuilder.
		Clone().
		PrebuiltWorkloadLabel("wl1").
		Label(kueue.MultiKueueOriginLabel, "origin1").
		MakePodGroupWrappers(groupSize)

	podGroupWithWlAnnotations := basePodBuilder.
		Clone().
		MakePodGroupWrappersWithWorkloadAnnotations(groupSize)

	workerPodGroupWithAnnotations := basePodBuilder.
		Clone().
		PrebuiltWorkloadAnnotation("wl1").
		Label(kueue.MultiKueueOriginLabel, "origin1").
		MakePodGroupWrappersWithWorkloadAnnotations(groupSize)

	cases := map[string]struct {
		managersPods []corev1.Pod
		workerPods   []corev1.Pod

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError        error
		wantManagersPods []corev1.Pod
		wantWorkerPods   []corev1.Pod
		featureGates     map[featuregate.Feature]bool
	}{
		"sync creates missing remote pod": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote pod": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"keeps SchedulingGated condition on gated manager pod (avoids spurious autoscaler scale-up)": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonSchedulingGated,
					}).
					Obj(),
			},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodPending).
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonUnschedulable,
					}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			// The remote PodScheduled=False/Unschedulable condition must not overwrite
			// the local SchedulingGated one, otherwise the management cluster's
			// cluster-autoscaler treats the gated Pod as unschedulable and scales up.
			// Other status (the phase here) is still synced from the worker.
			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusPhase(corev1.PodPending).
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonSchedulingGated,
					}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodPending).
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonUnschedulable,
					}).
					Obj(),
			},
		},
		"overwrites SchedulingGated once the remote pod is scheduled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			// While the worker Pod is unscheduled the manager Pod keeps its local
			// SchedulingGated condition (see the case above, which avoids a spurious
			// autoscaler scale-up). Once the worker Pod reports PodScheduled=True, that
			// condition is synced through to the manager Pod so its status reflects
			// reality instead of showing SchedulingGated while the phase is Running.
			// The manager Pod stays gated in its spec.
			managersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonSchedulingGated,
					}).
					Obj(),
			},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(
						corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					KueueSchedulingGate().
					StatusPhase(corev1.PodRunning).
					StatusConditions(
						corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(
						corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					).
					Obj(),
			},
		},
		"remote pod is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace})
			},
		},
		"pod managedBy multikueue": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
		},
		"sync creates missing remote pods of the group": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].DeepCopy(),
			},
		},
		"sync status from remote pod group": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote pod group is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].DeepCopy(),
				*podGroupWithWl[1].DeepCopy(),
				*podGroupWithWl[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace})
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].DeepCopy(),
				*podGroup[1].DeepCopy(),
				*podGroup[2].DeepCopy(),
			},
		},
		"sync creates missing remote pod, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					PrebuiltWorkloadAnnotation("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync creates missing remote pods of the group, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersPods: []corev1.Pod{
				*podGroupWithWlAnnotations[0].DeepCopy(),
				*podGroupWithWlAnnotations[1].DeepCopy(),
				*podGroupWithWlAnnotations[2].DeepCopy(),
			},
			wantWorkerPods: []corev1.Pod{
				*workerPodGroupWithAnnotations[0].DeepCopy(),
				*workerPodGroupWithAnnotations[1].DeepCopy(),
				*workerPodGroupWithAnnotations[2].DeepCopy(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			managerBuilder := utiltesting.NewClientBuilder().
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithIndex(&corev1.Pod{}, PodGroupNameCacheKey, IndexPodGroupName)
			managerBuilder = managerBuilder.WithLists(&corev1.PodList{Items: tc.managersPods})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersPods, func(w *corev1.Pod) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder().
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithIndex(&corev1.Pod{}, PodGroupNameCacheKey, IndexPodGroupName)
			workerBuilder = workerBuilder.WithLists(&corev1.PodList{Items: tc.workerPods})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotmanagersPods := &corev1.PodList{}
			if err := managerClient.List(ctx, gotmanagersPods); err != nil {
				t.Errorf("unexpected list manager's pods error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersPods, gotmanagersPods.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's pods (-want/+got):\n%s", diff)
				}
			}

			gotWorkerPods := &corev1.PodList{}
			if err := workerClient.List(ctx, gotWorkerPods); err != nil {
				t.Errorf("unexpected list worker's pod error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerPods, gotWorkerPods.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's pod (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
