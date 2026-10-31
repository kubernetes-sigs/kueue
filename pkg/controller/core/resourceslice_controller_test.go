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

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func testResourceSliceConfig() *configapi.Configuration {
	selector := resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}}
	return &configapi.Configuration{
		Resources: &configapi.Resources{
			DeviceClassMappings: []configapi.DeviceClassMapping{
				{
					Name:             "example.com/a-capacity",
					DeviceClassNames: []corev1.ResourceName{"class-a"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{
							Name: "gpu", Driver: "driver-a.example.com", DeviceSelector: selector,
						}},
					},
				},
				{
					Name:             "example.com/b-capacity",
					DeviceClassNames: []corev1.ResourceName{"class-b"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Capacity: &configapi.DeviceClassCapacitySource{
							Name: "example.com/capacity", Driver: "driver-b.example.com", DeviceSelector: selector,
						}},
					},
				},
				{
					Name:             "example.com/shared",
					DeviceClassNames: []corev1.ResourceName{"class-shared"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Capacity: &configapi.DeviceClassCapacitySource{
							Name: "example.com/capacity", Driver: "driver-a.example.com", DeviceSelector: selector,
						}},
						{Capacity: &configapi.DeviceClassCapacitySource{
							Name: "example.com/capacity", Driver: "driver-b.example.com", DeviceSelector: selector,
						}},
					},
				},
			},
		},
	}
}

func TestResourceSliceDriverEventRequests(t *testing.T) {
	r := NewResourceSliceReconciler(nil, nil, testResourceSliceConfig(), nil)
	a := &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Spec: resourcev1.ResourceSliceSpec{Driver: "driver-a.example.com"}}
	b := &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Spec: resourcev1.ResourceSliceSpec{Driver: "driver-b.example.com"}}
	unknown := &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: "unknown"}, Spec: resourcev1.ResourceSliceSpec{Driver: "other.example.com"}}

	if got := r.mapResourceSliceToDriver(context.Background(), a); len(got) != 1 || got[0].Name != "driver-a.example.com" || got[0].Namespace != "" {
		t.Fatalf("driver-A event requests: %v", got)
	}
	if got := r.mapResourceSliceToDriver(context.Background(), unknown); len(got) != 0 {
		t.Fatalf("unconfigured driver requests: %v", got)
	}
	if got := r.mapResourceSliceToDriver(context.Background(), b); len(got) != 1 || got[0].Name != "driver-b.example.com" {
		t.Fatalf("driver-B event requests: %v", got)
	}
	if !r.Create(event.TypedCreateEvent[*resourcev1.ResourceSlice]{Object: a}) ||
		!r.Update(event.TypedUpdateEvent[*resourcev1.ResourceSlice]{ObjectOld: a, ObjectNew: b}) ||
		!r.Update(event.TypedUpdateEvent[*resourcev1.ResourceSlice]{ObjectOld: a, ObjectNew: unknown}) ||
		!r.Update(event.TypedUpdateEvent[*resourcev1.ResourceSlice]{ObjectOld: unknown, ObjectNew: b}) ||
		!r.Delete(event.TypedDeleteEvent[*resourcev1.ResourceSlice]{Object: b}) {
		t.Fatal("configured driver event should pass predicate")
	}
	if r.Create(event.TypedCreateEvent[*resourcev1.ResourceSlice]{Object: unknown}) ||
		r.Delete(event.TypedDeleteEvent[*resourcev1.ResourceSlice]{Object: unknown}) ||
		r.Update(event.TypedUpdateEvent[*resourcev1.ResourceSlice]{ObjectOld: unknown, ObjectNew: unknown}) {
		t.Fatal("unconfigured driver event should be filtered")
	}
}

func TestResourceSliceDriverMappedClusterQueues(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	client := utiltesting.NewFakeClient()
	cache := schdcache.New(client)
	queue := qcache.NewManagerForUnitTests(client, cache)
	r := NewResourceSliceReconciler(queue, cache, testResourceSliceConfig(), nil)
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq-a").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/a-capacity", "10").Obj()).Obj(),
		utiltestingapi.MakeClusterQueue("cq-b").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/b-capacity", "10").Obj()).Obj(),
		utiltestingapi.MakeClusterQueue("cq-shared").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/shared", "10").Obj()).Obj(),
		utiltestingapi.MakeClusterQueue("cq-both").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/a-capacity", "10").Resource("example.com/b-capacity", "10").Obj()).Obj(),
	}
	for _, cq := range clusterQueues {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("add %s: %v", cq.Name, err)
		}
	}
	for _, tc := range []struct {
		driver string
		want   sets.Set[kueue.ClusterQueueReference]
	}{
		{"driver-a.example.com", sets.New[kueue.ClusterQueueReference]("cq-a", "cq-shared", "cq-both")},
		{"driver-b.example.com", sets.New[kueue.ClusterQueueReference]("cq-b", "cq-shared", "cq-both")},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			got := cache.ClusterQueuesForResources(r.quotaResourcesByDriver[tc.driver])
			if !got.Equal(tc.want) {
				t.Errorf("affected ClusterQueues: got %v, want %v", sets.List(got), sets.List(tc.want))
			}
		})
	}
	if got := cache.ClusterQueuesForResources(r.quotaResourcesByDriver["other.example.com"]); got.Len() != 0 {
		t.Fatalf("unconfigured driver matches ClusterQueues: %v", got)
	}
	if len(r.quotaResourcesByDriver) != 2 ||
		!r.quotaResourcesByDriver["driver-a.example.com"].Has(corev1.ResourceName("example.com/shared")) ||
		!r.quotaResourcesByDriver["driver-b.example.com"].Has(corev1.ResourceName("example.com/shared")) {
		t.Fatal("shared mapping missing from one or more drivers")
	}
}

func TestResourceSliceReconcileRetriesOnlyAffectedDriver(t *testing.T) {
	for _, tc := range []struct {
		name             string
		driver           string
		wantInadmissible map[kueue.ClusterQueueReference][]workload.Reference
	}{
		{
			name:   "driver A does not retry driver B",
			driver: "driver-a.example.com",
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {workload.NewReference("ns", "b-wl")},
			},
		},
		{
			name:   "driver B does not retry driver A",
			driver: "driver-b.example.com",
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-a": {workload.NewReference("ns", "a-wl")},
			},
		},
		{
			name:   "unknown driver does not retry any queue",
			driver: "unknown.example.com",
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-a": {workload.NewReference("ns", "a-wl")},
				"cq-b": {workload.NewReference("ns", "b-wl")},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
			cqA := utiltestingapi.MakeClusterQueue("cq-a").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/a-capacity", "10").Obj()).Obj()
			cqB := utiltestingapi.MakeClusterQueue("cq-b").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/b-capacity", "10").Obj()).Obj()
			lqA := utiltestingapi.MakeLocalQueue("lq-a", "ns").ClusterQueue("cq-a").Obj()
			lqB := utiltestingapi.MakeLocalQueue("lq-b", "ns").ClusterQueue("cq-b").Obj()
			cl := utiltesting.NewClientBuilder().
				WithObjects(
					utiltesting.MakeNamespace("ns"),
					flavor, lqA, lqB,
					utiltestingapi.MakeWorkload("a-wl", "ns").Queue("lq-a").Request("example.com/a-capacity", "1").Obj(),
					utiltestingapi.MakeWorkload("b-wl", "ns").Queue("lq-b").Request("example.com/b-capacity", "1").Obj(),
				).Build()
			cache := schdcache.New(cl)
			cache.AddOrUpdateResourceFlavor(log, flavor)
			queues, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cache,
				qcache.WithPreemptionExpectations(preemptexpectations.New()))
			for _, cq := range []*kueue.ClusterQueue{cqA, cqB} {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
				if err := queues.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
			}
			for _, lq := range []*kueue.LocalQueue{lqA, lqB} {
				if err := queues.AddLocalQueue(ctx, lq); err != nil {
					t.Fatal(err)
				}
			}
			// Ignore notifications emitted while setting up the queues.
			requeuer.ProcessRequeues(ctx)
			heads := queues.Heads(ctx)
			if len(heads) != 2 {
				t.Fatalf("popped workload heads = %d, want 2", len(heads))
			}
			for _, head := range heads {
				if !queues.RequeueWorkload(ctx, &head.Info, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota) {
					t.Fatalf("could not make workload %s inadmissible", head.Info.Obj.Name)
				}
			}

			r := NewResourceSliceReconciler(queues, cache, testResourceSliceConfig(), nil)
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: tc.driver}}); err != nil {
				t.Fatal(err)
			}
			requeuer.ProcessRequeues(ctx)
			if diff := cmp.Diff(tc.wantInadmissible, queues.DumpInadmissible()); diff != "" {
				t.Errorf("unexpected inadmissible Workloads (-want,+got):\n%s", diff)
			}
		})
	}
}
