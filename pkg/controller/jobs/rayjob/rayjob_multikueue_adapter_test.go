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

package rayjob

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
	utiltestingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
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

	rayJobBuilder := utiltestingrayjob.MakeJob("rayjob1", TestNamespace).Suspend(false)

	// elasticRayJobBuilder is workload-slicing enabled with in-tree autoscaling:
	// the worker is the source of truth for worker replicas via its child
	// RayCluster.
	elasticRayJobBuilder := rayJobBuilder.Clone().
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		EnableInTreeAutoscaling()

	// childRayCluster models the RayCluster KubeRay created for the RayJob on
	// the worker cluster, resized by the autoscaler.
	childRayCluster := func(name string, workerReplicas int32, generation int64) *rayv1.RayCluster {
		rc := utiltestingraycluster.MakeCluster(name, TestNamespace).
			FirstWorkerGroupReplicas(workerReplicas, 1, 5).
			Obj()
		rc.Generation = generation
		return rc
	}

	cases := map[string]struct {
		managersRayJobs   []rayv1.RayJob
		workerRayJobs     []rayv1.RayJob
		workerRayClusters []rayv1.RayCluster

		operation func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error

		wantError           error
		wantManagersRayJobs []rayv1.RayJob
		wantWorkerRayJobs   []rayv1.RayJob
		featureGates        map[featuregate.Feature]bool
	}{
		"sync creates missing remote rayjob": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote rayjob": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			workerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
		},
		"sync status from remote while local rayjob is suspended": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Suspend(true).
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
		},
		"remote rayjob is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace})
			},
		},
		"job with wrong managedBy is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
		},

		"job managedBy multikueue": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
		},
		"missing job is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"sync creates missing remote rayjob, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					PrebuiltWorkloadAnnotation("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		// The autoscaler resized the child RayCluster on the worker; the child's
		// per-group counts and generation are reflected onto the manager RayJob as
		// annotations (consumed by the manager's PodSet derivation and
		// workload-slice naming).
		"autoscaling elastic rayjob reflects the child's worker counts onto the manager": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.DeepCopy(),
			},
			workerRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					RayClusterNameStatus("rayjob1-raycluster").
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*childRayCluster("rayjob1-raycluster", 3, 7),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.Clone().
					Annotation(raycluster.MultiKueueRuntimePodSetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":3}]`).
					Annotation(raycluster.RayClusterGenerationAnnotation, "7").
					RayClusterNameStatus("rayjob1-raycluster").
					Obj(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					RayClusterNameStatus("rayjob1-raycluster").
					Obj(),
			},
		},
		// A suspended remote is being stopped by the worker's Kueue; its state
		// must not be reflected back.
		"autoscaling elastic rayjob skips reflection while the remote is suspended": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.DeepCopy(),
			},
			workerRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.Clone().
					Suspend(true).
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					RayClusterNameStatus("rayjob1-raycluster").
					Obj(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*childRayCluster("rayjob1-raycluster", 3, 7),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersRayJobs: []rayv1.RayJob{
				// Status is copied, but no annotations are reflected.
				*elasticRayJobBuilder.Clone().
					RayClusterNameStatus("rayjob1-raycluster").
					Obj(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.Clone().
					Suspend(true).
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					RayClusterNameStatus("rayjob1-raycluster").
					Obj(),
			},
		},
		// Before KubeRay creates the child RayCluster there is nothing to
		// reflect; the sync is a no-op (label repoint still applies).
		"autoscaling elastic rayjob without a child yet only repoints the prebuilt label": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.DeepCopy(),
			},
			workerRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.Clone().
					PrebuiltWorkloadLabel("stale-wl").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl2", "origin1")
				return err
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.DeepCopy(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*elasticRayJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl2").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayJobList{Items: tc.managersRayJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayJobs, func(w *rayv1.RayJob) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&rayv1.RayJobList{Items: tc.workerRayJobs}, &rayv1.RayClusterList{Items: tc.workerRayClusters})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := ray.NewMKAdapter(
				copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
				ray.WithElasticReplicaSync(elasticRuntimeSync()),
			)

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersRayJobs := &rayv1.RayJobList{}
			if err := managerClient.List(ctx, gotManagersRayJobs); err != nil {
				t.Errorf("unexpected list manager's rayjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersRayJobs, gotManagersRayJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's rayjobs (-want/+got):\n%s", diff)
				}
			}

			gotWorkerRayJobs := &rayv1.RayJobList{}
			if err := workerClient.List(ctx, gotWorkerRayJobs); err != nil {
				t.Errorf("unexpected list worker's rayjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayJobs, gotWorkerRayJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's rayjobs (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
