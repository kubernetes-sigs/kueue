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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	baseRayServiceBuilder := func() *rayv1.RayService {
		return &rayv1.RayService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rayservice1",
				Namespace: TestNamespace,
			},
			Spec: rayv1.RayServiceSpec{
				RayClusterSpec: rayv1.RayClusterSpec{
					HeadGroupSpec: rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "head"}},
							},
						},
					},
				},
			},
		}
	}

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
				*baseRayServiceBuilder(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayServices: []rayv1.RayService{
				*baseRayServiceBuilder(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				func() rayv1.RayService {
					svc := baseRayServiceBuilder()
					svc.Labels = map[string]string{
						constants.PrebuiltWorkloadLabel: "wl1",
						kueue.MultiKueueOriginLabel:     "origin1",
					}
					return *svc
				}(),
			},
		},
		"sync status from remote rayservice": {
			managersRayServices: []rayv1.RayService{
				*baseRayServiceBuilder(),
			},
			workerRayServices: []rayv1.RayService{
				func() rayv1.RayService {
					svc := baseRayServiceBuilder()
					svc.Labels = map[string]string{
						constants.PrebuiltWorkloadLabel: "wl1",
						kueue.MultiKueueOriginLabel:     "origin1",
					}
					svc.Status.ServiceStatus = rayv1.Running
					return *svc
				}(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayServices: []rayv1.RayService{
				func() rayv1.RayService {
					svc := baseRayServiceBuilder()
					svc.Status.ServiceStatus = rayv1.Running
					return *svc
				}(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				func() rayv1.RayService {
					svc := baseRayServiceBuilder()
					svc.Labels = map[string]string{
						constants.PrebuiltWorkloadLabel: "wl1",
						kueue.MultiKueueOriginLabel:     "origin1",
					}
					svc.Status.ServiceStatus = rayv1.Running
					return *svc
				}(),
			},
		},
		"remote rayservice is deleted": {
			workerRayServices: []rayv1.RayService{
				func() rayv1.RayService {
					svc := baseRayServiceBuilder()
					svc.Labels = map[string]string{
						constants.PrebuiltWorkloadLabel: "wl1",
						kueue.MultiKueueOriginLabel:     "origin1",
					}
					return *svc
				}(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace})
			},
			wantWorkerRayServices: []rayv1.RayService{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptorFuncs)
			managerBuilder = managerBuilder.WithLists(&rayv1.RayServiceList{Items: tc.managersRayServices})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayServices, func(w *rayv1.RayService) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptorFuncs)
			workerBuilder = workerBuilder.WithLists(&rayv1.RayServiceList{Items: tc.workerRayServices})
			workerClient := workerBuilder.Build()

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersServices := &rayv1.RayServiceList{}
			if err := managerClient.List(ctx, gotManagersServices); err != nil {
				t.Errorf("unexpected list manager's services error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersRayServices, gotManagersServices.Items, objCheckOpts); diff != "" {
					t.Errorf("unexpected manager's services (-want/+got):\n%s", diff)
				}
			}

			gotWorkerServices := &rayv1.RayServiceList{}
			if err := workerClient.List(ctx, gotWorkerServices); err != nil {
				t.Errorf("unexpected list worker's services error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayServices, gotWorkerServices.Items, objCheckOpts); diff != "" {
					t.Errorf("unexpected worker's services (-want/+got):\n%s", diff)
				}
			}
		})
	}
}

func TestIsJobManagedByKueue(t *testing.T) {
	testCases := map[string]struct {
		rayService *rayv1.RayService
		want       bool
		wantReason string
	}{
		"managed by kueue": {
			rayService: &rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: TestNamespace,
					Labels: map[string]string{
						constants.QueueLabel: "queue",
					},
				},
			},
			want: true,
		},
		"not managed by kueue": {
			rayService: &rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: TestNamespace,
				},
			},
			want:       false,
			wantReason: "RayService is not managed by Kueue (missing queue label)",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			_ = rayv1.AddToScheme(scheme)
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.rayService)
			client := builder.Build()

			adapter := &multiKueueAdapter{}
			got, reason, err := adapter.IsJobManagedByKueue(ctx, client, types.NamespacedName{
				Name:      tc.rayService.Name,
				Namespace: tc.rayService.Namespace,
			})

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsJobManagedByKueue() = %v, want %v", got, tc.want)
			}
			if tc.wantReason != "" && reason != tc.wantReason {
				t.Errorf("IsJobManagedByKueue() reason = %v, want %v", reason, tc.wantReason)
			}
		})
	}
}

var interceptorFuncs = interceptor.Funcs{
	SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		return client.Status().Patch(ctx, obj, patch, opts...)
	},
}
