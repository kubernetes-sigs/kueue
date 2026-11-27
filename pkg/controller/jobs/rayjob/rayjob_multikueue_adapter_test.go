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
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	errStatusPatch := errors.New("status patch failed")
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	rayJobBuilder := utiltestingrayjob.MakeJob("rayjob1", TestNamespace).Suspend(false)

	integrations := jobframework.NewIntegrationManager()
	if err := RegisterIntegration(integrations); err != nil {
		t.Fatal(err)
	}
	registered, ok := integrations.GetIntegration(FrameworkName)
	if !ok || registered.MultiKueueAdapter == nil {
		t.Fatal("RayJob integration did not register a MultiKueue adapter")
	}
	disabledAdapter := ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy)

	managerJob := rayJobBuilder.Clone()
	managerJob.UID = "manager-job-uid"
	recreatedJob := managerJob.Clone()
	recreatedJob.UID = "new-manager-job-uid"
	oldSlice := utiltestingapi.MakeWorkload("old-slice", TestNamespace).
		ControllerReference(gvk, managerJob.Name, string(managerJob.UID)).Obj()
	ownerlessSlice := oldSlice.DeepCopy()
	ownerlessSlice.OwnerReferences = nil

	workerJob := rayJobBuilder.Clone().PrebuiltWorkloadLabel(oldSlice.Name).Label(kueue.MultiKueueOriginLabel, "origin1")
	succeeded := workerJob.Clone().JobStatus(rayv1.JobStatusSucceeded).JobDeploymentStatus(rayv1.JobDeploymentStatusComplete)
	failedJob := workerJob.Clone().JobStatus(rayv1.JobStatusFailed).JobDeploymentStatus(rayv1.JobDeploymentStatusComplete)
	failedDeployment := failedJob.Clone().JobDeploymentStatus(rayv1.JobDeploymentStatusFailed)
	running := workerJob.Clone().JobDeploymentStatus(rayv1.JobDeploymentStatusRunning)
	repointed := succeeded.Clone().PrebuiltWorkloadLabel("replacement-slice")
	foreign := succeeded.Clone().Label(kueue.MultiKueueOriginLabel, "another-origin")
	annotated := rayJobBuilder.Clone().PrebuiltWorkloadAnnotation(oldSlice.Name).Label(kueue.MultiKueueOriginLabel, "origin1")
	annotated.Status = succeeded.Status

	jobs := func(job *utiltestingrayjob.JobWrapper) []rayv1.RayJob {
		return []rayv1.RayJob{*job.DeepCopy()}
	}
	managerWithStatus := func(worker *utiltestingrayjob.JobWrapper) []rayv1.RayJob {
		job := managerJob.DeepCopy()
		job.Status = worker.Status
		return []rayv1.RayJob{*job}
	}

	type operation func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error
	terminalSync := func(slice *kueue.Workload, wantSynced bool) operation {
		return func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
			t.Helper()
			syncer, ok := adapter.(jobframework.MultiKueueTerminalJobStatusSyncer)
			if !ok {
				t.Fatal("RayJob adapter does not implement MultiKueueTerminalJobStatusSyncer")
			}
			synced, err := syncer.SyncTerminalJobStatus(ctx, managerClient, workerClient, slice, "origin1")
			if synced != wantSynced {
				t.Errorf("SyncTerminalJobStatus synced = %v, want %v", synced, wantSynced)
			}
			return err
		}
	}

	cases := map[string]struct {
		managersRayJobs []rayv1.RayJob
		workerRayJobs   []rayv1.RayJob

		operation operation

		wantError           error
		wantManagersRayJobs []rayv1.RayJob
		wantWorkerRayJobs   []rayv1.RayJob
		featureGates        map[featuregate.Feature]bool
		adapter             jobframework.MultiKueueAdapter
		managerPatchError   error
		wantStatusPatches   int
	}{
		"sync creates missing remote rayjob": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
			wantStatusPatches: 1,
			featureGates:      map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
			wantStatusPatches: 1,
			featureGates:      map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
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
		"terminal: successful completion": {
			wantStatusPatches:   1,
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(succeeded),
			operation:           terminalSync(oldSlice, true),
			wantManagersRayJobs: managerWithStatus(succeeded),
			wantWorkerRayJobs:   jobs(succeeded),
		},
		"terminal: workload reference in annotation": {
			wantStatusPatches:   1,
			featureGates:        map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(annotated),
			operation:           terminalSync(oldSlice, true),
			wantManagersRayJobs: managerWithStatus(annotated),
			wantWorkerRayJobs:   jobs(annotated),
		},
		"terminal: failed job with complete deployment": {
			wantStatusPatches:   1,
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(failedJob),
			operation:           terminalSync(oldSlice, true),
			wantManagersRayJobs: managerWithStatus(failedJob),
			wantWorkerRayJobs:   jobs(failedJob),
		},
		"terminal: failed deployment": {
			wantStatusPatches:   1,
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(failedDeployment),
			operation:           terminalSync(oldSlice, true),
			wantManagersRayJobs: managerWithStatus(failedDeployment),
			wantWorkerRayJobs:   jobs(failedDeployment),
		},
		"terminal: running status is ignored": {
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(running),
			operation:           terminalSync(oldSlice, false),
			wantManagersRayJobs: jobs(managerJob),
			wantWorkerRayJobs:   jobs(running),
		},
		"terminal: already repointed worker is ignored": {
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(repointed),
			operation:           terminalSync(oldSlice, false),
			wantManagersRayJobs: jobs(managerJob),
			wantWorkerRayJobs:   jobs(repointed),
		},
		"terminal: foreign worker is rejected": {
			wantError:           jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(foreign),
			operation:           terminalSync(oldSlice, false),
			wantManagersRayJobs: jobs(managerJob),
			wantWorkerRayJobs:   jobs(foreign),
		},
		"terminal: missing worker job": {
			managersRayJobs:     jobs(managerJob),
			operation:           terminalSync(oldSlice, false),
			wantManagersRayJobs: jobs(managerJob),
		},
		"terminal: missing manager job": {
			workerRayJobs:     jobs(succeeded),
			operation:         terminalSync(oldSlice, false),
			wantWorkerRayJobs: jobs(succeeded),
		},
		"terminal: recreated manager job": {
			managersRayJobs:     jobs(recreatedJob),
			workerRayJobs:       jobs(succeeded),
			operation:           terminalSync(oldSlice, false),
			wantManagersRayJobs: jobs(recreatedJob),
			wantWorkerRayJobs:   jobs(succeeded),
		},
		"terminal: slice has no controller owner": {
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(succeeded),
			operation:           terminalSync(ownerlessSlice, false),
			wantManagersRayJobs: jobs(managerJob),
			wantWorkerRayJobs:   jobs(succeeded),
		},
		"terminal: manager already has terminal status": {
			managersRayJobs:     managerWithStatus(succeeded),
			workerRayJobs:       jobs(succeeded),
			operation:           terminalSync(oldSlice, false),
			wantManagersRayJobs: managerWithStatus(succeeded),
			wantWorkerRayJobs:   jobs(succeeded),
		},
		"terminal: recovery disabled": {
			adapter:             disabledAdapter,
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(succeeded),
			operation:           terminalSync(oldSlice, false),
			wantManagersRayJobs: jobs(managerJob),
			wantWorkerRayJobs:   jobs(succeeded),
		},
		"sync unchanged status does not patch manager": {
			managersRayJobs: managerWithStatus(succeeded),
			workerRayJobs:   jobs(succeeded),
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, oldSlice.Name, "origin1")
				return err
			},
			wantManagersRayJobs: managerWithStatus(succeeded),
			wantWorkerRayJobs:   jobs(succeeded),
		},
		"sync manager status patch failure propagates": {
			managersRayJobs: jobs(managerJob),
			workerRayJobs:   jobs(succeeded),
			operation: func(t *testing.T, ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, oldSlice.Name, "origin1")
				return err
			},
			managerPatchError:   errStatusPatch,
			wantStatusPatches:   1,
			wantError:           errStatusPatch,
			wantManagersRayJobs: jobs(managerJob),
			wantWorkerRayJobs:   jobs(succeeded),
		},
		"terminal: manager status patch failure propagates": {
			managersRayJobs:     jobs(managerJob),
			workerRayJobs:       jobs(succeeded),
			operation:           terminalSync(oldSlice, false),
			managerPatchError:   errStatusPatch,
			wantStatusPatches:   1,
			wantError:           errStatusPatch,
			wantManagersRayJobs: jobs(managerJob),
			wantWorkerRayJobs:   jobs(succeeded),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			statusPatches := 0
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
				SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
				SubResourcePatch: func(ctx context.Context, c client.Client, subResource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					statusPatches++
					if tc.managerPatchError != nil {
						return tc.managerPatchError
					}
					return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
				},
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

			adapter := registered.MultiKueueAdapter
			if tc.adapter != nil {
				adapter = tc.adapter
			}

			gotErr := tc.operation(t, ctx, adapter, managerClient, workerClient)
			if statusPatches != tc.wantStatusPatches {
				t.Errorf("manager status PATCH calls = %d, want %d", statusPatches, tc.wantStatusPatches)
			}

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
