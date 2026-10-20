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
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestAutoscalingRayServiceInPlaceUpdate(t *testing.T) {
	patchError := errors.New("serve config patch failed")
	cases := map[string]struct {
		runtimeChanged bool
		syncDisabled   bool
		suspended      bool
		patchError     error
	}{
		"forwards config while reflecting an autoscaler resize without repointing the old slice": {runtimeChanged: true},
		"forwards config after repointing the admitted slice":                                    {},
		"disabled spec sync still reflects runtime state":                                        {runtimeChanged: true, syncDisabled: true},
		"suspended manager does not forward config":                                              {suspended: true},
		"returns config patch errors for retry":                                                  {patchError: patchError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:       true,
				features.MultiKueueRayInTreeAutoscaling:     true,
				features.MultiKueueRemoteSpecSync:           !tc.syncDisabled,
				features.KubeRayServiceUsingTopLevelSuspend: true,
			})
			managerService := testingrayservice.MakeService("service", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				EnableInTreeAutoscaling().
				Suspend(tc.suspended).
				WithServeConfigV2("new-config").Obj()
			managerService.UID = "manager-uid"
			workerService := managerService.DeepCopy()
			workerService.UID = "worker-uid"
			workerService.Spec.Suspend = false
			workerService.Spec.ServeConfigV2 = "old-config"
			workerService.Status.ActiveServiceStatus.RayClusterName = "active-cluster"
			jobframework.SetPrebuiltWorkloadName(workerService, "old-slice")
			child := &rayv1.RayCluster{
				Name:       "active-cluster",
				Namespace:  "ns",
				UID:        "child-uid",
				Generation: 2,
				Spec:       *workerService.Spec.RayClusterSpec.DeepCopy(),
			}
			child.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](3)
			child.Spec.WorkerGroupSpecs[0].MinReplicas = ptr.To[int32](0)
			child.Spec.WorkerGroupSpecs[0].MaxReplicas = ptr.To[int32](5)
			runtimeState := ray.FetchResult{Counts: raycluster.WorkerGroupPodCounts(&child.Spec), Revision: "child-uid-2"}
			if !tc.runtimeChanged {
				raycluster.SetRuntimeWorkerStateAnnotations(managerService, runtimeState)
			}
			wantManager := managerService.DeepCopy()
			wantManager.Status = workerService.Status
			if !tc.suspended {
				raycluster.SetRuntimeWorkerStateAnnotations(wantManager, runtimeState)
			}
			wantWorker := workerService.DeepCopy()
			if !tc.suspended && !tc.runtimeChanged {
				jobframework.SetPrebuiltWorkloadName(wantWorker, "admitted-slice")
			}
			if !tc.suspended && !tc.syncDisabled && tc.patchError == nil {
				wantWorker.Spec.ServeConfigV2 = managerService.Spec.ServeConfigV2
			}
			managerClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).
				WithObjects(managerService).WithStatusSubresource(managerService).Build()
			workerClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).
				WithObjects(workerService, child).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if service, ok := obj.(*rayv1.RayService); ok && service.Spec.ServeConfigV2 == "new-config" && tc.patchError != nil {
							return tc.patchError
						}
						return c.Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			adapter := ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
				ray.WithElasticReplicaSync(elasticRuntimeSync()),
				ray.WithRemoteSpecSync[*rayv1.RayService, rayv1.RayService](remoteSpecSyncer{}))
			ctx, _ := utiltesting.ContextWithLog(t)
			key := client.ObjectKeyFromObject(managerService)
			_, err := adapter.SyncJob(ctx, managerClient, workerClient, key, "admitted-slice", "origin")
			if diff := cmp.Diff(tc.patchError, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("unexpected sync error (-want/+got):\n%s", diff)
			}
			opts := cmp.Options{cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"), cmpopts.EquateEmpty()}
			gotManager, gotWorker := &rayv1.RayService{}, &rayv1.RayService{}
			if err := managerClient.Get(ctx, key, gotManager); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(wantManager, gotManager, opts...); diff != "" {
				t.Errorf("unexpected manager service (-want/+got):\n%s", diff)
			}
			if err := workerClient.Get(ctx, key, gotWorker); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(wantWorker, gotWorker, opts...); diff != "" {
				t.Errorf("unexpected worker service (-want/+got):\n%s", diff)
			}
			gotChild := &rayv1.RayCluster{}
			if err := workerClient.Get(ctx, client.ObjectKeyFromObject(child), gotChild); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(child, gotChild, opts...); diff != "" {
				t.Errorf("unexpected active cluster (-want/+got):\n%s", diff)
			}
		})
	}
}
