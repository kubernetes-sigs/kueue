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

package raycluster

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
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
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

	rayClusterBuilder := utiltestingraycluster.MakeCluster("raycluster1", TestNamespace).Suspend(false)

	// elasticBuilder is workload-slicing enabled (elastic-job annotation set).
	elasticBuilder := rayClusterBuilder.Clone().
		SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)

	// scaleUpManager is the manager copy after a scale-up to 3 worker replicas.
	// scaleUpWorkloadName is the workload name of the slice that reflects it; the
	// MultiKueue controller calls SyncJob with this name once that slice is admitted.
	scaleUpManager := elasticBuilder.Clone().ScaleFirstWorkerGroup(3).Obj()
	scaleUpWorkloadName := jobframework.GenerateWorkloadNameWithExtra(
		scaleUpManager.Name, scaleUpManager.UID, gvk, GetWorkloadNameExtraPart(scaleUpManager))

	cases := map[string]struct {
		managersRayClusters []rayv1.RayCluster
		managersWorkloads   []kueue.Workload
		workerRayClusters   []rayv1.RayCluster

		operation func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error

		wantError               error
		wantManagersRayClusters []rayv1.RayCluster
		wantWorkerRayClusters   []rayv1.RayCluster
		featureGates            map[featuregate.Feature]bool
	}{
		"sync creates missing remote raycluster": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote raycluster": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
		},
		"sync status from remote while local raycluster is suspended": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					Suspend(true).
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
		},
		"remote raycluster is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace})
			},
		},
		"raycluster with wrong managedBy is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
		},

		"raycluster managedBy multikueue": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
		},
		"missing raycluster is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"sync creates missing remote raycluster, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadAnnotation("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"elastic scale-down propagates reduced worker replicas to the remote": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().ScaleFirstWorkerGroup(1).Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(3).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().ScaleFirstWorkerGroup(1).Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(1).
					Obj(),
			},
		},
		// A change confined to NumOfHosts still alters the effective per-group pod
		// count that needElasticSync compares, so it must be propagated. Here the
		// effective count decreases (4 -> 2), which avoids the scale-up stale guard,
		// isolating the NumOfHosts propagation with Replicas held equal.
		"elastic NumOfHosts change propagates to the remote worker group": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().ScaleFirstWorkerGroup(2).WithNumOfHosts("workers-group-0", 1).Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(2).
					WithNumOfHosts("workers-group-0", 2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().ScaleFirstWorkerGroup(2).WithNumOfHosts("workers-group-0", 1).Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(2).
					WithNumOfHosts("workers-group-0", 1).
					Obj(),
			},
		},
		"elastic scale-up with the current slice name propagates replicas and updates the workload label": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*scaleUpManager.DeepCopy(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(1).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, scaleUpWorkloadName, "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*scaleUpManager.DeepCopy(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel(scaleUpWorkloadName).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(3).
					Obj(),
			},
		},
		"elastic scale-up observing a stale old slice is skipped": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().ScaleFirstWorkerGroup(3).Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("stale-wl").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(1).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "stale-wl", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().ScaleFirstWorkerGroup(3).Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("stale-wl").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(1).
					Obj(),
			},
		},
		// With in-tree autoscaling the worker is the source of truth: an
		// autoscaler-driven scale-up on the remote copy (3 replicas) is written
		// back onto the manager's RayCluster (which was at 1), so the manager's
		// slicing machinery can re-reserve quota. The worker copy is left
		// untouched by this reconcile.
		"autoscaling elastic sync writes the worker's autoscaled replicas back to the manager": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 5).
					Obj(),
			},
			managersWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", TestNamespace).Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(3, 1, 5).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(3, 1, 5).
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(3, 1, 5).
					Obj(),
			},
		},
		// A suspended remote copy's replicas were restored by the worker's Kueue
		// while stopping, not set by the autoscaler, so they must not be written
		// back to the manager.
		"autoscaling elastic sync skips write-back while the remote is suspended": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 5).
					Obj(),
			},
			managersWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", TestNamespace).Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(3, 1, 5).
					Suspend(true).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 5).
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(3, 1, 5).
					Suspend(true).
					Obj(),
			},
		},
		// A reconcile of a slice that already finished (e.g. the pre-resize slice
		// during a scale-up handover) must not write back replicas or repoint the
		// remote's prebuilt-workload label back at the finished slice.
		"autoscaling elastic sync ignores a finished slice's reconcile": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 5).
					Obj(),
			},
			managersWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", TestNamespace).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "Succeeded"}).
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl2").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(3, 1, 5).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 5).
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl2").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(3, 1, 5).
					Obj(),
			},
		},
		// A remote replica count outside the manager-declared [min, max] cannot
		// come from the autoscaler and must not reach the manager's spec.
		"autoscaling elastic sync ignores out-of-bounds remote replicas": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 5).
					Obj(),
			},
			managersWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", TestNamespace).Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(99, 1, 5).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 5).
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*elasticBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(99, 1, 5).
					Obj(),
			},
		},
		"non-elastic raycluster does not sync worker replicas even with the feature enabled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().ScaleFirstWorkerGroup(1).Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(3).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().ScaleFirstWorkerGroup(1).Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					ScaleFirstWorkerGroup(3).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayClusterList{Items: tc.managersRayClusters}, &kueue.WorkloadList{Items: tc.managersWorkloads})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayClusters, func(w *rayv1.RayCluster) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&rayv1.RayClusterList{Items: tc.workerRayClusters})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
				ray.WithElasticReplicaSync(elasticReplicaSync()))

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersRayClusters := &rayv1.RayClusterList{}
			if err := managerClient.List(ctx, gotManagersRayClusters); err != nil {
				t.Errorf("unexpected list manager's rayclusters error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersRayClusters, gotManagersRayClusters.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's rayclusters (-want/+got):\n%s", diff)
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
