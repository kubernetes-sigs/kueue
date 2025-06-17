package rayservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	rayServiceBuilder := utiltestingrayservice.MakeService("rayservice1", TestNamespace).Suspend(false)

	cases := map[string]struct {
		managersRayServices []rayv1.RayService
		workerRayServices   []rayv1.RayService

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError               error
		wantManagersRayServices []rayv1.RayService
		wantWorkerRayServices   []rayv1.RayService
	}{
		"sync creates missing remote rayservice": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote rayservice": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
		},
		"skip to sync status from remote suspended rayservice": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
		},
		"remote rayservice is deleted": {
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace})
			},
		},
		"rayservice with wrong managedBy is not considered managed": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
		},

		"rayservice managedBy multikueue": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
		},
		"missing rayservice is not considered managed": {
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayServiceList{Items: tc.managersRayServices})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayServices, func(w *rayv1.RayService) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&rayv1.RayServiceList{Items: tc.workerRayServices})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersRayServices := &rayv1.RayServiceList{}
			if err := managerClient.List(ctx, gotManagersRayServices); err != nil {
				t.Errorf("unexpected list manager's rayservices error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersRayServices, gotManagersRayServices.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's rayservices (-want/+got):\n%s", diff)
				}
			}

			gotWorkerRayServices := &rayv1.RayServiceList{}
			if err := workerClient.List(ctx, gotWorkerRayServices); err != nil {
				t.Errorf("unexpected list worker's rayserivces error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayServices, gotWorkerRayServices.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's rayservices (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
