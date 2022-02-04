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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
)

func TestFIFOQueue(t *testing.T) {
	q := newQueue()
	now := metav1.Now()
	ws := []*kueue.QueuedWorkload{
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
		t.Errorf("Poped workload %q want %q", got.Obj.Name, "before")
	}
	q.PushOrUpdate(&kueue.QueuedWorkload{
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
		t.Errorf("Poped workload %q want %q", got.Obj.Name, "after")
	}

	q.Delete(&kueue.QueuedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "now"},
	})
	got = q.Pop()
	if got != nil {
		t.Errorf("Queue is not empty, poped workload %q", got.Obj.Name)
	}
}
