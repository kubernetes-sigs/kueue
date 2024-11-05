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

package tfjob

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	kfutiltesting "sigs.k8s.io/kueue/pkg/util/testingjobs/tfjob"
)

const (
	TestNamespace = "ns"
)

func TestMultikueueAdapter(t *testing.T) {
	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	tfJobBuilder := kfutiltesting.MakeTFJob("tfjob1", TestNamespace).Queue("queue").Suspend(false)

	cases := map[string]struct {
		managersTFJobs []kftraining.TFJob
		workerTFJobs   []kftraining.TFJob

		operation func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error

		wantError          error
		wantManagersTFJobs []kftraining.TFJob
		wantWorkerTFJobs   []kftraining.TFJob
	}{
		"sync creates missing remote tfjob": {
			managersTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "tfjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().Obj(),
			},
			wantWorkerTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote tfjob": {
			managersTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().Obj(),
			},
			workerTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "tfjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().
					StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote tfjob is deleted": {
			workerTFJobs: []kftraining.TFJob{
				*tfJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "tfjob1", Namespace: TestNamespace})
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(kftraining.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&kftraining.TFJobList{Items: tc.managersTFJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersTFJobs, func(w *kftraining.TFJob) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(kftraining.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&kftraining.TFJobList{Items: tc.workerTFJobs})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := kubeflowjob.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk)

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersTFJob := &kftraining.TFJobList{}
			if err := managerClient.List(ctx, gotManagersTFJob); err != nil {
				t.Errorf("unexpected list manager's tfjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersTFJobs, gotManagersTFJob.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's tfjobs (-want/+got):\n%s", diff)
				}
			}

			gotWorkerTFJobs := &kftraining.TFJobList{}
			if err := workerClient.List(ctx, gotWorkerTFJobs); err != nil {
				t.Errorf("unexpected list worker's tfjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerTFJobs, gotWorkerTFJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's tfjobs (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
