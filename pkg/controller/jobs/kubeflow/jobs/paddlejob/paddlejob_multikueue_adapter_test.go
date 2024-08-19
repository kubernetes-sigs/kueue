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

package paddlejob

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

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	kfutiltesting "sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
)

const (
	TestNamespace = "ns"
)

func TestMultikueueAdapter(t *testing.T) {
	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	paddleJobBuilder := kfutiltesting.MakePaddleJob("paddlejob1", TestNamespace).Queue("queue").Suspend(false)

	cases := map[string]struct {
		managersPaddleJobs []kftraining.PaddleJob
		workerPaddleJobs   []kftraining.PaddleJob

		operation func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error

		wantError              error
		wantManagersPaddleJobs []kftraining.PaddleJob
		wantWorkerPaddleJobs   []kftraining.PaddleJob
	}{
		"sync creates missing remote PaddleJob": {
			managersPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().Obj(),
			},

			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "paddlejob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().Obj(),
			},
			wantWorkerPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueuealpha.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote PaddleJob": {
			managersPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().Obj(),
			},
			workerPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueuealpha.MultiKueueOriginLabel, "origin1").
					StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "paddlejob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().
					StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueuealpha.MultiKueueOriginLabel, "origin1").
					StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote PaddleJob is deleted": {
			workerPaddleJobs: []kftraining.PaddleJob{
				*paddleJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueuealpha.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "paddlejob1", Namespace: TestNamespace})
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(kftraining.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&kftraining.PaddleJobList{Items: tc.managersPaddleJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersPaddleJobs, func(w *kftraining.PaddleJob) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(kftraining.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&kftraining.PaddleJobList{Items: tc.workerPaddleJobs})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := kubeflowjob.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk)

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersPaddleJob := &kftraining.PaddleJobList{}
			if err := managerClient.List(ctx, gotManagersPaddleJob); err != nil {
				t.Errorf("unexpected list manager's PaddleJobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersPaddleJobs, gotManagersPaddleJob.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's PaddleJobs (-want/+got):\n%s", diff)
				}
			}

			gotWorkerPaddleJobs := &kftraining.PaddleJobList{}
			if err := workerClient.List(ctx, gotWorkerPaddleJobs); err != nil {
				t.Errorf("unexpected list worker's PaddleJobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerPaddleJobs, gotWorkerPaddleJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's PaddleJobs (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
