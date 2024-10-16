/*
Copyright 2024 The Kubernetes Authors.

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

package jobset

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
)

const (
	TestNamespace = "ns"
)

func TestMultikueueAdapter(t *testing.T) {
	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	baseJobSetBuilder := utiltestingjobset.MakeJobSet("jobset1", TestNamespace)
	baseJobSetManagedByKueueBuilder := baseJobSetBuilder.DeepCopy().ManagedBy(kueue.MultiKueueControllerName)

	cases := map[string]struct {
		managersJobSets []jobsetapi.JobSet
		workerJobSets   []jobsetapi.JobSet

		operation func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error

		wantError           error
		wantManagersJobSets []jobsetapi.JobSet
		wantWorkerJobSets   []jobsetapi.JobSet
	}{

		"sync creates missing remote jobset": {
			managersJobSets: []jobsetapi.JobSet{
				*baseJobSetManagedByKueueBuilder.DeepCopy().Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "jobset1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersJobSets: []jobsetapi.JobSet{
				*baseJobSetManagedByKueueBuilder.DeepCopy().Obj(),
			},
			wantWorkerJobSets: []jobsetapi.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote jobset": {
			managersJobSets: []jobsetapi.JobSet{
				*baseJobSetManagedByKueueBuilder.DeepCopy().Obj(),
			},
			workerJobSets: []jobsetapi.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobsStatus(
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-1",
							Ready:     1,
							Succeeded: 1,
						},
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-2",
							Ready:     3,
							Succeeded: 0,
						},
					).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "jobset1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersJobSets: []jobsetapi.JobSet{
				*baseJobSetManagedByKueueBuilder.DeepCopy().
					JobsStatus(
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-1",
							Ready:     1,
							Succeeded: 1,
						},
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-2",
							Ready:     3,
							Succeeded: 0,
						},
					).
					Obj(),
			},
			wantWorkerJobSets: []jobsetapi.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobsStatus(
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-1",
							Ready:     1,
							Succeeded: 1,
						},
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-2",
							Ready:     3,
							Succeeded: 0,
						},
					).
					Obj(),
			},
		},
		"remote jobset is deleted": {
			workerJobSets: []jobsetapi.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobsStatus(
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-1",
							Ready:     1,
							Succeeded: 1,
						},
						jobsetapi.ReplicatedJobStatus{
							Name:      "replicated-job-2",
							Ready:     3,
							Succeeded: 0,
						},
					).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "jobset1", Namespace: TestNamespace})
			},
		},
		"missing jobset is not considered managed": {
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "jobset1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"job with wrong managedBy is not considered managed": {
			managersJobSets: []jobsetapi.JobSet{
				*baseJobSetBuilder.DeepCopy().Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "jobset1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersJobSets: []jobsetapi.JobSet{
				*baseJobSetBuilder.DeepCopy().Obj(),
			},
		},

		"job managedBy multikueue": {
			managersJobSets: []jobsetapi.JobSet{
				*baseJobSetManagedByKueueBuilder.DeepCopy().Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "jobset1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersJobSets: []jobsetapi.JobSet{
				*baseJobSetManagedByKueueBuilder.DeepCopy().Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(jobsetapi.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&jobsetapi.JobSetList{Items: tc.managersJobSets})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersJobSets, func(w *jobsetapi.JobSet) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(jobsetapi.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&jobsetapi.JobSetList{Items: tc.workerJobSets})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multikueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersJobSets := &jobsetapi.JobSetList{}
			if err := managerClient.List(ctx, gotManagersJobSets); err != nil {
				t.Errorf("unexpected list manager's jobsets error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersJobSets, gotManagersJobSets.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's jobsets (-want/+got):\n%s", diff)
				}
			}

			gotWorkerJobSets := &jobsetapi.JobSetList{}
			if err := workerClient.List(ctx, gotWorkerJobSets); err != nil {
				t.Errorf("unexpected list worker's jobsets error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerJobSets, gotWorkerJobSets.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's jobsets (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
