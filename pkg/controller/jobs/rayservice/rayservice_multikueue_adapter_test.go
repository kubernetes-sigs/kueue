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

package rayservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	utiltestingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
	autoscalingRayServiceBuilder := rayServiceBuilder.Clone().
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		EnableInTreeAutoscaling()
	activeRayClusterBuilder := utiltestingraycluster.MakeCluster("active-cluster", TestNamespace).
		UID("active-cluster-uid").
		Generation(2).
		FirstWorkerGroupReplicas(3, 0, 5)
	autoscalingGates := map[featuregate.Feature]bool{
		features.WorkloadIdentifierAnnotations:      false,
		features.ElasticJobsViaWorkloadSlices:       true,
		features.MultiKueueRayInTreeAutoscaling:     true,
		features.KubeRayServiceUsingTopLevelSuspend: true,
	}
	errServeConfigPatch := errors.New("serve config patch failed")

	cases := map[string]struct {
		managersRayServices []rayv1.RayService
		workerRayServices   []rayv1.RayService
		workerRayClusters   []rayv1.RayCluster
		workerPatch         func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error

		operation func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error

		wantError               error
		wantManagersRayServices []rayv1.RayService
		wantWorkerRayServices   []rayv1.RayService
		wantWorkerRayClusters   []rayv1.RayCluster
		featureGates            map[featuregate.Feature]bool
	}{
		"sync creates missing remote rayservice": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync forwards a serveConfigV2 change to the existing remote rayservice": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().WithServeConfigV2("new-config").Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithServeConfigV2("old-config").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().WithServeConfigV2("new-config").Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithServeConfigV2("new-config").
					Obj(),
			},
		},
		"autoscaling: sync forwards serveConfigV2 while reflecting an autoscaler resize, without repointing the old slice": {
			featureGates: autoscalingGates,
			managersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().WithServeConfigV2("new-config").Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "admitted-slice", "origin1")
				return err
			},
			wantManagersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":3}]`).
					Annotation(raycluster.RayClusterGenerationAnnotation, "active-cluster-uid-2").
					WithServeConfigV2("new-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("new-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
		},
		"autoscaling: sync forwards serveConfigV2 after repointing the admitted slice": {
			featureGates: autoscalingGates,
			managersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":3}]`).
					Annotation(raycluster.RayClusterGenerationAnnotation, "active-cluster-uid-2").
					WithServeConfigV2("new-config").
					Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "admitted-slice", "origin1")
				return err
			},
			wantManagersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":3}]`).
					Annotation(raycluster.RayClusterGenerationAnnotation, "active-cluster-uid-2").
					WithServeConfigV2("new-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			// The manager already reflects the active RayCluster's worker counts, so
			// the sync sees no runtime change and repoints the remote onto the
			// admitted slice being synced.
			wantWorkerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("admitted-slice").
					WithServeConfigV2("new-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
		},
		"autoscaling: sync reflects an autoscaler resize but does not forward serveConfigV2 when MultiKueueRemoteSpecSync is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadIdentifierAnnotations:      false,
				features.ElasticJobsViaWorkloadSlices:       true,
				features.MultiKueueRayInTreeAutoscaling:     true,
				features.KubeRayServiceUsingTopLevelSuspend: true,
				features.MultiKueueRemoteSpecSync:           false,
			},
			managersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().WithServeConfigV2("new-config").Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "admitted-slice", "origin1")
				return err
			},
			wantManagersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":3}]`).
					Annotation(raycluster.RayClusterGenerationAnnotation, "active-cluster-uid-2").
					WithServeConfigV2("new-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
		},
		"autoscaling: sync does not forward serveConfigV2 while the manager rayservice is suspended": {
			featureGates: autoscalingGates,
			managersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().Suspend(true).WithServeConfigV2("new-config").Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "admitted-slice", "origin1")
				return err
			},
			wantManagersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					Suspend(true).
					WithServeConfigV2("new-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
		},
		"autoscaling: sync returns the serveConfigV2 patch error after repointing the admitted slice": {
			featureGates: autoscalingGates,
			managersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":3}]`).
					Annotation(raycluster.RayClusterGenerationAnnotation, "active-cluster-uid-2").
					WithServeConfigV2("new-config").
					Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("old-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
			workerPatch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if service, ok := obj.(*rayv1.RayService); ok && service.Spec.ServeConfigV2 == "new-config" {
					return errServeConfigPatch
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "admitted-slice", "origin1")
				return err
			},
			wantError: errServeConfigPatch,
			wantManagersRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":3}]`).
					Annotation(raycluster.RayClusterGenerationAnnotation, "active-cluster-uid-2").
					WithServeConfigV2("new-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*autoscalingRayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("admitted-slice").
					WithServeConfigV2("old-config").
					ActiveRayClusterName("active-cluster").
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{*activeRayClusterBuilder.Clone().Obj()},
		},
		"sync status from remote rayservice": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
		},
		"sync status from remote while local rayservice is suspended": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Suspend(true).
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
		},
		"remote rayservice is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace})
			},
		},
		"job with wrong managedBy is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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

		"job managedBy multikueue": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
		"missing job is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"sync creates missing remote rayservice, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadAnnotation("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
				SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
			})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayServiceList{Items: tc.managersRayServices})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayServices, func(w *rayv1.RayService) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
				SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
				Patch:            tc.workerPatch,
			})
			workerBuilder = workerBuilder.WithLists(
				&rayv1.RayServiceList{Items: tc.workerRayServices},
				&rayv1.RayClusterList{Items: tc.workerRayClusters},
			)
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
				ray.WithElasticReplicaSync(elasticRuntimeSync()),
				ray.WithRemoteSpecSync[*rayv1.RayService, rayv1.RayService](remoteSpecSyncer{}))

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
				t.Errorf("unexpected list worker's rayservices error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayServices, gotWorkerRayServices.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's rayservices (-want/+got):\n%s", diff)
				}
			}

			gotWorkerRayClusters := &rayv1.RayClusterList{}
			if err := workerClient.List(ctx, gotWorkerRayClusters); err != nil {
				t.Errorf("unexpected list worker's rayclusters error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayClusters, gotWorkerRayClusters.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's rayclusters (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
