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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
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

	cases := map[string]struct {
		managersRayJobs []rayv1.RayJob
		workerRayJobs   []rayv1.RayJob

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
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
				SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
			})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayJobList{Items: tc.managersRayJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayJobs, func(w *rayv1.RayJob) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
				SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
			})
			workerBuilder = workerBuilder.WithLists(&rayv1.RayJobList{Items: tc.workerRayJobs})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy)

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

func TestSyncTerminalJobStatus(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.WorkloadIdentifierAnnotations: false,
	})
	const (
		oldSlice = "old-slice"
		newSlice = "replacement-slice"
		origin   = "origin1"
	)
	tests := map[string]struct {
		deploymentStatus rayv1.JobDeploymentStatus
		jobStatus        rayv1.JobStatus
		prebuilt         string
		remoteOrigin     string
		noRemote         bool
		wantSynced       bool
		wantError        bool
	}{
		"successful completion": {
			deploymentStatus: rayv1.JobDeploymentStatusComplete,
			jobStatus:        rayv1.JobStatusSucceeded,
			prebuilt:         oldSlice,
			remoteOrigin:     origin,
			wantSynced:       true,
		},
		"failed job with complete deployment": {
			deploymentStatus: rayv1.JobDeploymentStatusComplete,
			jobStatus:        rayv1.JobStatusFailed,
			prebuilt:         oldSlice,
			remoteOrigin:     origin,
			wantSynced:       true,
		},
		"failed deployment": {
			deploymentStatus: rayv1.JobDeploymentStatusFailed,
			jobStatus:        rayv1.JobStatusFailed,
			prebuilt:         oldSlice,
			remoteOrigin:     origin,
			wantSynced:       true,
		},
		"running status is ignored": {
			deploymentStatus: rayv1.JobDeploymentStatusRunning,
			prebuilt:         oldSlice,
			remoteOrigin:     origin,
		},
		"already repointed worker is ignored": {
			deploymentStatus: rayv1.JobDeploymentStatusComplete,
			jobStatus:        rayv1.JobStatusSucceeded,
			prebuilt:         newSlice,
			remoteOrigin:     origin,
		},
		"foreign worker is rejected": {
			deploymentStatus: rayv1.JobDeploymentStatusComplete,
			jobStatus:        rayv1.JobStatusSucceeded,
			prebuilt:         oldSlice,
			remoteOrigin:     "another-origin",
			wantError:        true,
		},
		"missing remote is ignored": {
			noRemote: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			local := utiltestingrayjob.MakeJob("rayjob1", TestNamespace).Suspend(false).Obj()
			managerClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).
				WithObjects(local).
				WithStatusSubresource(local).
				Build()
			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme)
			var remote *rayv1.RayJob
			if !tc.noRemote {
				remote = utiltestingrayjob.MakeJob("rayjob1", TestNamespace).
					Suspend(false).
					PrebuiltWorkloadLabel(tc.prebuilt).
					Label(kueue.MultiKueueOriginLabel, tc.remoteOrigin).
					JobStatus(tc.jobStatus).
					JobDeploymentStatus(tc.deploymentStatus).
					Obj()
				workerBuilder = workerBuilder.WithObjects(remote)
			}
			workerClient := workerBuilder.Build()
			adapter := ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
				ray.WithTerminalStatusSync(func(job *rayv1.RayJob) bool {
					return job.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusComplete ||
						job.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusFailed
				}))
			syncer := adapter.(jobframework.MultiKueueTerminalJobStatusSyncer)
			key := types.NamespacedName{Name: local.Name, Namespace: local.Namespace}
			gotSynced, err := syncer.SyncTerminalJobStatus(ctx, managerClient, workerClient, key, oldSlice, origin)
			if (err != nil) != tc.wantError {
				t.Fatalf("SyncTerminalJobStatus error = %v, wantError = %v", err, tc.wantError)
			}
			if gotSynced != tc.wantSynced {
				t.Errorf("SyncTerminalJobStatus synced = %v, want %v", gotSynced, tc.wantSynced)
			}
			managerAfter := &rayv1.RayJob{}
			if err := managerClient.Get(ctx, key, managerAfter); err != nil {
				t.Fatal(err)
			}
			wantStatus := local.Status
			if tc.wantSynced {
				wantStatus = remote.Status
			}
			if diff := cmp.Diff(wantStatus, managerAfter.Status); diff != "" {
				t.Errorf("unexpected manager RayJob status (-want/+got): %s", diff)
			}
			if !tc.noRemote {
				workerAfter := &rayv1.RayJob{}
				if err := workerClient.Get(ctx, key, workerAfter); err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(remote.Spec, workerAfter.Spec); diff != "" {
					t.Errorf("remote RayJob spec changed (-want/+got): %s", diff)
				}
				if got := jobframework.PrebuiltWorkloadNameFor(workerAfter); got != tc.prebuilt {
					t.Errorf("remote prebuilt workload changed to %q; want %q", got, tc.prebuilt)
				}
			}
		})
	}
}
