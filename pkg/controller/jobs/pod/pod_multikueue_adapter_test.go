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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
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
		Label(constants.PrebuiltWorkloadLabel, "wl1").
		Label(kueue.MultiKueueOriginLabel, "origin1").
		MakePodGroupWrappers(groupSize)

	cases := map[string]struct {
		managersPods []corev1.Pod
		workerPods   []corev1.Pod

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError        error
		wantManagersPods []corev1.Pod
		wantWorkerPods   []corev1.Pod
	}{
		"sync creates missing remote pod": {
			managersPods: []corev1.Pod{
				*basePodBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote pod": {
			managersPods: []corev1.Pod{
				*basePodBuilder.DeepCopy(),
			},
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote pod is deleted": {
			workerPods: []corev1.Pod{
				*basePodBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace})
			},
		},
		"pod managedBy multikueue": {
			managersPods: []corev1.Pod{
				*basePodBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: basePodName, Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersPods: []corev1.Pod{
				*basePodBuilder.Clone().Obj(),
			},
		},
		"sync creates missing remote pods of the group": {
			managersPods: []corev1.Pod{
				*podGroup[0].Clone().Obj(),
				*podGroup[1].Clone().Obj(),
				*podGroup[2].Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].Clone().Obj(),
				*podGroup[1].Clone().Obj(),
				*podGroup[2].Clone().Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Obj(),
				*podGroupWithWl[1].Clone().Obj(),
				*podGroupWithWl[2].Clone().Obj(),
			},
		},
		"sync status from remote pod group": {
			managersPods: []corev1.Pod{
				*podGroup[0].Clone().Obj(),
				*podGroup[1].Clone().Obj(),
				*podGroup[2].Clone().Obj(),
			},
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Obj(),
				*podGroupWithWl[1].Clone().Obj(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersPods: []corev1.Pod{
				*podGroup[0].Clone().Obj(),
				*podGroup[1].Clone().Obj(),
				*podGroup[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Obj(),
				*podGroupWithWl[1].Clone().Obj(),
				*podGroupWithWl[2].Clone().
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote pod group is deleted": {
			workerPods: []corev1.Pod{
				*podGroupWithWl[0].Clone().Obj(),
				*podGroupWithWl[1].Clone().Obj(),
				*podGroupWithWl[2].Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: podGroup[0].Obj().Name, Namespace: TestNamespace})
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&corev1.PodList{Items: tc.managersPods})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersPods, func(w *corev1.Pod) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
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
