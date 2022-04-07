/*
Copyright 2022 The Kubernetes Authors.

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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
)

const (
	lowPriority  = 0
	highPriority = 1000
)

func TestFIFOClusterQueue(t *testing.T) {
	q, err := newClusterQueue(&kueue.ClusterQueue{
		Spec: kueue.ClusterQueueSpec{
			QueueingStrategy: kueue.StrictFIFO,
		},
	})
	if err != nil {
		t.Fatalf("Failed creating ClusterQueue %v", err)
	}
	now := metav1.Now()
	ws := []*kueue.Workload{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "now",
				CreationTimestamp: now,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "before",
				CreationTimestamp: metav1.NewTime(now.Add(-time.Second)),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "after",
				CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
			},
		},
	}
	for _, w := range ws {
		q.PushOrUpdate(w)
	}
	got := q.Pop()
	if got == nil {
		t.Fatal("Queue is empty")
	}
	if got.Obj.Name != "before" {
		t.Errorf("Popped workload %q want %q", got.Obj.Name, "before")
	}
	q.PushOrUpdate(&kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "after",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
		},
	})
	got = q.Pop()
	if got == nil {
		t.Fatal("Queue is empty")
	}
	if got.Obj.Name != "after" {
		t.Errorf("Popped workload %q want %q", got.Obj.Name, "after")
	}

	q.Delete(&kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "now"},
	})
	got = q.Pop()
	if got != nil {
		t.Errorf("Queue is not empty, popped workload %q", got.Obj.Name)
	}
}

func TestStrictFIFO(t *testing.T) {
	t1 := time.Now()
	t2 := t1.Add(time.Second)
	for _, tt := range []struct {
		name     string
		w1       *kueue.Workload
		w2       *kueue.Workload
		expected string
	}{
		{
			name: "w1.priority is higher than w2.priority",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "highPriority",
					Priority:          pointer.Int32(highPriority),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "lowPriority",
					Priority:          pointer.Int32(lowPriority),
				},
			},
			expected: "w1",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
			},
			expected: "w1",
		},
		{
			name: "p1.priority is lower than p2.priority and w1.create time is earlier than w2.create time",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "lowPriority",
					Priority:          pointer.Int32(lowPriority),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "highPriority",
					Priority:          pointer.Int32(highPriority),
				},
			},
			expected: "w2",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q, err := newClusterQueue(&kueue.ClusterQueue{
				Spec: kueue.ClusterQueueSpec{
					QueueingStrategy: kueue.StrictFIFO,
				},
			})
			if err != nil {
				t.Fatalf("Failed creating ClusterQueue %v", err)
			}

			q.PushOrUpdate(tt.w1)
			q.PushOrUpdate(tt.w2)

			got := q.Pop()
			if got == nil {
				t.Fatal("Queue is empty")
			}
			if got.Obj.Name != tt.expected {
				t.Errorf("Popped workload %q want %q", got.Obj.Name, tt.expected)
			}
		})
	}
}
