/*
Copyright 2022 Google LLC.

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

package queue

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/workload"
)

// TestOrphans verifies that pods added before adding the queue are preserved
// and they persist after the queue recreation.
func TestAddQueue(t *testing.T) {
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&kueue.QueuedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "a"},
			Spec:       kueue.QueuedWorkloadSpec{QueueName: "foo"},
		},
		&kueue.QueuedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "b"},
			Spec:       kueue.QueuedWorkloadSpec{QueueName: "bar"},
		},
		&kueue.QueuedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "c"},
			Spec:       kueue.QueuedWorkloadSpec{QueueName: "foo"},
		},
		&kueue.QueuedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "d"},
			Spec: kueue.QueuedWorkloadSpec{
				QueueName:        "foo",
				AssignedCapacity: "capacity",
			},
		},
	).Build()
	manager := NewManager(client)
	q := kueue.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "foo"},
	}
	if err := manager.AddQueue(context.Background(), &q); err != nil {
		t.Fatalf("Failed adding queue: %v", err)
	}
	qImpl := manager.queues["foo"]
	workloadNames := popWorkloadNames(qImpl)
	sort.Strings(workloadNames)
	if diff := cmp.Diff([]string{"a", "c"}, workloadNames); diff != "" {
		t.Errorf("Unexpected items in queue foo (-want,+got):\n%s", diff)
	}
}

func TestAddWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
	queues := []*kueue.Queue{
		{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "bar"}},
	}
	for _, q := range queues {
		if err := manager.AddQueue(context.Background(), q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	cases := []struct {
		workload  *kueue.QueuedWorkload
		wantAdded bool
	}{
		{
			workload: &kueue.QueuedWorkload{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing_queue",
				},
				Spec: kueue.QueuedWorkloadSpec{QueueName: "foo"},
			},
			wantAdded: true,
		},
		{
			workload: &kueue.QueuedWorkload{
				ObjectMeta: metav1.ObjectMeta{
					Name: "non_existing_queue",
				},
				Spec: kueue.QueuedWorkloadSpec{QueueName: "baz"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.workload.Name, func(t *testing.T) {
			if added := manager.AddWorkload(tc.workload); added != tc.wantAdded {
				t.Errorf("AddWorkload returned %t, want %t", added, tc.wantAdded)
			}
		})
	}
}

func TestUpdateWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	now := metav1.Now()
	cases := map[string]struct {
		queues         []string
		workloads      []*kueue.QueuedWorkload
		update         func(*kueue.QueuedWorkload)
		wantUpdated    bool
		wantQueueOrder map[string][]string
	}{
		"in queue": {
			queues: []string{"foo"},
			workloads: []*kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "a",
						CreationTimestamp: now,
					},
					Spec: kueue.QueuedWorkloadSpec{QueueName: "foo"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "b",
						CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
					},
					Spec: kueue.QueuedWorkloadSpec{QueueName: "foo"},
				},
			},
			update: func(w *kueue.QueuedWorkload) {
				w.CreationTimestamp = metav1.NewTime(now.Add(time.Minute))
			},
			wantUpdated: true,
			wantQueueOrder: map[string][]string{
				"foo": {"b", "a"},
			},
		},
		"between queues": {
			queues: []string{"foo", "bar"},
			workloads: []*kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{QueueName: "foo"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "b"},
					Spec:       kueue.QueuedWorkloadSpec{QueueName: "foo"},
				},
			},
			update: func(w *kueue.QueuedWorkload) {
				w.Spec.QueueName = "bar"
			},
			wantUpdated: true,
			wantQueueOrder: map[string][]string{
				"foo": {"b"},
				"bar": {"a"},
			},
		},
		"to non existent queue": {
			queues: []string{"foo"},
			workloads: []*kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{QueueName: "foo"},
				},
			},
			update: func(w *kueue.QueuedWorkload) {
				w.Spec.QueueName = "bar"
			},
			wantUpdated: false,
			wantQueueOrder: map[string][]string{
				"foo": nil,
			},
		},
		"from non existing queue": {
			queues: []string{"foo"},
			workloads: []*kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{QueueName: "bar"},
				},
			},
			update: func(w *kueue.QueuedWorkload) {
				w.Spec.QueueName = "foo"
			},
			wantUpdated: true,
			wantQueueOrder: map[string][]string{
				"foo": {"a"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
			for _, qName := range tc.queues {
				if err := manager.AddQueue(context.Background(), &kueue.Queue{
					ObjectMeta: metav1.ObjectMeta{Name: qName},
				}); err != nil {
					t.Fatalf("Adding queue %q: %v", qName, err)
				}
			}
			for _, w := range tc.workloads {
				manager.AddWorkload(w)
			}
			wl := tc.workloads[0].DeepCopy()
			prevQueue := wl.Spec.QueueName
			tc.update(wl)
			if updated := manager.UpdateWorkload(wl, prevQueue); updated != tc.wantUpdated {
				t.Errorf("UpdatedWorkload returned %t, want %t", updated, tc.wantUpdated)
			}
			queueOrder := make(map[string][]string)
			for name, q := range manager.queues {
				queueOrder[name] = popWorkloadNames(q)
			}
			if diff := cmp.Diff(tc.wantQueueOrder, queueOrder); diff != "" {
				t.Errorf("Elements poped in the wrong order from queues (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestHeads(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	now := time.Now().Truncate(time.Second)

	queues := []kueue.Queue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: kueue.QueueSpec{
				Capacity: "fooCap",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bar"},
			Spec: kueue.QueueSpec{
				Capacity: "barCap",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "baz"},
			Spec: kueue.QueueSpec{
				Capacity: "bazCap",
			},
		},
	}
	workloads := []kueue.QueuedWorkload{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "a",
				CreationTimestamp: metav1.NewTime(now.Add(time.Hour)),
			},
			Spec: kueue.QueuedWorkloadSpec{QueueName: "foo"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "b",
				CreationTimestamp: metav1.NewTime(now),
			},
			Spec: kueue.QueuedWorkloadSpec{QueueName: "bar"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "c",
				CreationTimestamp: metav1.NewTime(now),
			},
			Spec: kueue.QueuedWorkloadSpec{QueueName: "foo"},
		},
	}
	manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
	for _, q := range queues {
		manager.AddQueue(ctx, &q)
	}
	for _, wl := range workloads {
		wl := wl
		manager.AddWorkload(&wl)
	}
	wantHeads := []workload.Info{
		{
			Obj:      &workloads[1],
			Capacity: "barCap",
		},
		{
			Obj:      &workloads[2],
			Capacity: "fooCap",
		},
	}

	heads := manager.Heads(ctx)
	sort.Slice(heads, func(i, j int) bool {
		return heads[i].Obj.Name < heads[j].Obj.Name
	})
	if diff := cmp.Diff(wantHeads, heads); diff != "" {
		t.Errorf("GetHeads returned wrong heads (-want,+got):\n%s", diff)
	}

}

// TestHeadAsync ensures that Heads call is blocked until the queues are filled
// asynchronously.
func TestHeadsAsync(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	now := time.Now().Truncate(time.Second)
	wl := kueue.QueuedWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "a",
			CreationTimestamp: metav1.NewTime(now),
		},
		Spec: kueue.QueuedWorkloadSpec{QueueName: "foo"},
	}
	q := kueue.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "foo"},
		Spec: kueue.QueueSpec{
			Capacity: "fooCap",
		},
	}
	wantHeads := []workload.Info{
		{
			Obj:      &wl,
			Capacity: "fooCap",
		},
	}

	t.Run("AddQueue", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&wl).Build()
		manager := NewManager(client)
		go func() {
			manager.AddQueue(ctx, &q)
		}()
		heads := manager.Heads(ctx)
		if diff := cmp.Diff(wantHeads, heads); diff != "" {
			t.Errorf("GetHeads returned wrong heads (-want,+got):\n%s", diff)
		}
	})

	t.Run("AddWorkload", func(t *testing.T) {
		manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
		manager.AddQueue(ctx, &q)
		go func() {
			manager.addWorkload(&wl)
		}()
		heads := manager.Heads(ctx)
		if diff := cmp.Diff(wantHeads, heads); diff != "" {
			t.Errorf("GetHeads returned wrong heads (-want,+got):\n%s", diff)
		}
	})
}

// TestHeadsCancelled ensures that the Heads call returns when the context is closed.
func TestHeadsCancelled(t *testing.T) {
	manager := NewManager(fake.NewClientBuilder().Build())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		cancel()
	}()
	manager.CleanUpOnContext(ctx)
	heads := manager.Heads(ctx)
	if len(heads) != 0 {
		t.Errorf("GetHeads returned elements, expected none")
	}
}

func popWorkloadNames(q *Queue) []string {
	var names []string
	for w := q.Pop(); w != nil; w = q.Pop() {
		names = append(names, w.Obj.Name)
	}
	return names
}
