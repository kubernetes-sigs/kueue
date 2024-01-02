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
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	lowPriority  int32 = 0
	highPriority int32 = 1000
)

func TestFIFOClusterQueue(t *testing.T) {
	q, err := newClusterQueue(
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFO,
			},
		},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.Eviction},
	)
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
		q.PushOrUpdate(workload.NewInfo(w))
	}
	got := q.Pop()
	if got == nil {
		t.Fatal("Queue is empty")
	}
	if got.Obj.Name != "before" {
		t.Errorf("Popped workload %q want %q", got.Obj.Name, "before")
	}
	wlInfo := workload.NewInfo(&kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "after",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
		},
	})
	q.PushOrUpdate(wlInfo)
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
	t3 := t2.Add(time.Second)
	for _, tt := range []struct {
		name             string
		w1               *kueue.Workload
		w2               *kueue.Workload
		workloadOrdering *workload.Ordering
		expected         string
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
					Priority:          ptr.To(highPriority),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "lowPriority",
					Priority:          ptr.To(lowPriority),
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
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time but w1 was evicted",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(t3),
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "by test",
						},
					},
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
			},
			expected: "w2",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time and w1 was evicted but kueue is configured to always use the creation timestamp",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(t3),
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "by test",
						},
					},
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
			},
			workloadOrdering: &workload.Ordering{
				PodsReadyRequeuingTimestamp: config.Creation,
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
					Priority:          ptr.To(lowPriority),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "highPriority",
					Priority:          ptr.To(highPriority),
				},
			},
			expected: "w2",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.workloadOrdering == nil {
				// The default ordering:
				tt.workloadOrdering = &workload.Ordering{PodsReadyRequeuingTimestamp: config.Eviction}
			}
			q, err := newClusterQueue(
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.StrictFIFO,
					},
				},
				*tt.workloadOrdering,
			)
			if err != nil {
				t.Fatalf("Failed creating ClusterQueue %v", err)
			}

			q.PushOrUpdate(workload.NewInfo(tt.w1))
			q.PushOrUpdate(workload.NewInfo(tt.w2))

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

func TestStrictFIFORequeueIfNotPresent(t *testing.T) {
	tests := map[RequeueReason]struct {
		wantInadmissible bool
	}{
		RequeueReasonFailedAfterNomination: {
			wantInadmissible: false,
		},
		RequeueReasonNamespaceMismatch: {
			wantInadmissible: true,
		},
		RequeueReasonGeneric: {
			wantInadmissible: false,
		},
	}

	for reason, test := range tests {
		t.Run(string(reason), func(t *testing.T) {
			cq, _ := newClusterQueueStrictFIFO(
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.StrictFIFO,
					},
				},
				workload.Ordering{PodsReadyRequeuingTimestamp: config.Eviction},
			)
			wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), reason); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			_, gotInadmissible := cq.(*ClusterQueueStrictFIFO).inadmissibleWorkloads[workload.Key(wl)]
			if test.wantInadmissible != gotInadmissible {
				t.Errorf("Got inadmissible after requeue %t, want %t", gotInadmissible, test.wantInadmissible)
			}

			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), reason); ok {
				t.Error("Re-queued a workload that was already present")
			}
		})
	}
}
