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

package appwrapper

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.IgnoreFields(awv1beta2.AppWrapperComponent{}, "Template"),
		cmpopts.EquateEmpty(),
	}

	baseAppWrapperBuilder := utiltestingaw.MakeAppWrapper("aw1", TestNamespace).
		Component(utiltestingaw.Component{
			Template: utiltestingjob.MakeJob("job", "ns").SetTypeMeta().Parallelism(2).Obj(),
		})
	baseAppWrapperManagedByKueueBuilder := baseAppWrapperBuilder.DeepCopy().ManagedBy(kueue.MultiKueueControllerName)

	cases := map[string]struct {
		managersAppWrappers []awv1beta2.AppWrapper
		workerAppWrappers   []awv1beta2.AppWrapper

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError               error
		wantManagersAppWrappers []awv1beta2.AppWrapper
		wantWorkerAppWrappers   []awv1beta2.AppWrapper
	}{

		"sync creates missing remote appwrapper": {
			managersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "aw1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().Obj(),
			},
			wantWorkerAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote appwrapper": {
			managersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().Suspend(false).Obj(),
			},
			workerAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					SetPhase(awv1beta2.AppWrapperSuspended).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "aw1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().
					Suspend(false).
					SetPhase(awv1beta2.AppWrapperSuspended).
					Obj(),
			},
			wantWorkerAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					SetPhase(awv1beta2.AppWrapperSuspended).
					Obj(),
			},
		},
		"status is not synced if local appwrapper is still suspended": {
			managersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().Suspend(true).Obj(),
			},
			workerAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					SetPhase(awv1beta2.AppWrapperSuspended).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "aw1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().
					Suspend(true).
					SetPhase(awv1beta2.AppWrapperEmpty).
					Obj(),
			},
			wantWorkerAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					SetPhase(awv1beta2.AppWrapperSuspended).
					Obj(),
			},
		},
		"remote appwrapper is deleted": {
			workerAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "aw1", Namespace: TestNamespace})
			},
		},
		"missing appwrapper is not considered managed": {
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "aw1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"job with wrong managedBy is not considered managed": {
			managersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "aw1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperBuilder.DeepCopy().Obj(),
			},
		},

		"job managedBy multikueue": {
			managersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "aw1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersAppWrappers: []awv1beta2.AppWrapper{
				*baseAppWrapperManagedByKueueBuilder.DeepCopy().Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(awv1beta2.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&awv1beta2.AppWrapperList{Items: tc.managersAppWrappers})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersAppWrappers, func(w *awv1beta2.AppWrapper) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(awv1beta2.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&awv1beta2.AppWrapperList{Items: tc.workerAppWrappers})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersAppWrappers := &awv1beta2.AppWrapperList{}
			if err := managerClient.List(ctx, gotManagersAppWrappers); err != nil {
				t.Errorf("unexpected list manager's appwrapper error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersAppWrappers, gotManagersAppWrappers.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's appwrapper (-want/+got):\n%s", diff)
				}
			}

			gotWorkerAppWrappers := &awv1beta2.AppWrapperList{}
			if err := workerClient.List(ctx, gotWorkerAppWrappers); err != nil {
				t.Errorf("unexpected list worker's appwrapper error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerAppWrappers, gotWorkerAppWrappers.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's appwrapper (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
