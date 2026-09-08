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

package core

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestDRAQueueUsesPreprocessingResourceSnapshot(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)
	cases := map[string]struct {
		requeueAfterBackoff bool
		preprocessingRead   int
	}{
		"initial DRA queue insertion":       {preprocessingRead: 1},
		"DRA queue insertion after backoff": {requeueAfterBackoff: true, preprocessingRead: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			wlBuilder := utiltestingapi.MakeWorkload("wl", "ns").Queue("queue")
			if tc.requeueAfterBackoff {
				wlBuilder.RequeueState(nil, &metav1.Time{Time: now.Add(-time.Minute)})
			}
			wl := wlBuilder.Obj()
			const gpu corev1.ResourceName = "example.com/gpu"
			lr := utiltesting.MakeLimitRange("defaults", "ns").WithValue("DefaultRequest", gpu, "1").Obj()
			dc := utiltesting.MakeDeviceClass("gpu.example.com").ExtendedResourceName(string(gpu)).Obj()
			reads := 0
			cl := utiltesting.NewClientBuilder().WithObjects(lr, dc).
				WithStatusSubresource(wl).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithIndex(&resourcev1.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex, indexer.IndexDeviceClassExtendedResourceName).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if err := c.List(ctx, list, opts...); err != nil {
							return err
						}
						if _, ok := list.(*corev1.LimitRangeList); ok {
							reads++
							if reads == tc.preprocessingRead {
								// DRA preprocesses 1 GPU; a fresh read when queueing would see 2.
								lr.Spec.Limits[0].DefaultRequest[gpu] = resource.MustParse("2")
								return c.Update(ctx, lr)
							}
						}
						return nil
					}}).Build()
			cache := schdcache.New(cl)
			queues := qcache.NewManagerForUnitTests(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			lq := utiltestingapi.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj()
			if err := queues.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if err := cl.Create(ctx, wl); err != nil {
				t.Fatal(err)
			}
			mapper := dra.NewResourceMapper()
			if err := mapper.PopulateFromConfiguration([]configapi.DeviceClassMapping{{Name: "logical-gpu", DeviceClassNames: []corev1.ResourceName{"gpu.example.com"}}}); err != nil {
				t.Fatal(err)
			}
			backedResources := dra.NewExtendedResourceCache()
			backedResources.Add(gpu, dc.Name)
			reconciler := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{}, WithDRAMapper(mapper), WithDRABackedResources(backedResources))
			reconciler.clock = testingclock.NewFakeClock(now)
			if tc.requeueAfterBackoff {
				if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}); err != nil {
					t.Fatal(err)
				}
			} else if _, _, _, err := reconciler.handleDRA(ctx, wl); err != nil {
				t.Fatal(err)
			}
			infos := queues.PendingWorkloadsInfo("cq")
			if len(infos) != 1 {
				t.Fatalf("queued Infos = %d, want 1", len(infos))
			}
			qty := infos[0].PodSpec(0).Containers[0].Resources.Requests[gpu]
			if qty.Cmp(resource.MustParse("1")) != 0 {
				t.Errorf("queued view uses %s GPU, but DRA processed 1", qty.String())
			}
			if got := infos[0].TotalRequests[0].Requests.ResourceValue("logical-gpu"); got != 1 {
				t.Errorf("logical quota = %d, want 1", got)
			}
			if got := infos[0].TotalRequests[0].Requests.ResourceValue(gpu); got != 0 {
				t.Errorf("replaced extended resource quota = %d, want 0", got)
			}
			if len(infos[0].Obj.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests) != 0 {
				t.Fatal("raw Workload contains defaults")
			}
		})
	}
}
