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

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/event"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func testResourceSliceConfig() *configapi.Configuration {
	return &configapi.Configuration{
		Resources: &configapi.Resources{
			DeviceClassMappings: []configapi.DeviceClassMapping{
				{
					Name: "example.com/a-capacity",
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{Driver: "driver-a.example.com"}},
					},
				},
				{
					Name: "example.com/b-capacity",
					Sources: []configapi.DeviceClassSourceConfig{
						{Capacity: &configapi.DeviceClassCapacitySource{Driver: "driver-b.example.com"}},
					},
				},
				{
					Name: "example.com/shared",
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{Driver: "driver-a.example.com"}},
						{Capacity: &configapi.DeviceClassCapacitySource{Driver: "driver-b.example.com"}},
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
