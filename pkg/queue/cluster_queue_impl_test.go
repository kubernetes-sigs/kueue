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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	defaultNamespace = "default"
)

func Test_PushOrUpdate(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	if cq.Pending() != 0 {
		t.Error("ClusterQueue should be empty")
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if cq.Pending() != 1 {
		t.Error("ClusterQueue should have one workload")
	}

	// Just used to validate the update operation.
	wl.ResourceVersion = "1"
	cq.PushOrUpdate(workload.NewInfo(wl))
	newWl := cq.Pop()
	if cq.Pending() != 0 || newWl.Obj.ResourceVersion != "1" {
		t.Error("failed to update a workload in ClusterQueue")
	}
}

func Test_Pop(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	now := time.Now()
	wl1 := workload.NewInfo(utiltesting.MakeWorkload("workload-1", defaultNamespace).Creation(now).Obj())
	wl2 := workload.NewInfo(utiltesting.MakeWorkload("workload-2", defaultNamespace).Creation(now.Add(time.Second)).Obj())
	if cq.Pop() != nil {
		t.Error("ClusterQueue should be empty")
	}
	cq.PushOrUpdate(wl1)
	cq.PushOrUpdate(wl2)
	newWl := cq.Pop()
	if newWl == nil || newWl.Obj.Name != "workload-1" {
		t.Error("failed to Pop workload")
	}
	newWl = cq.Pop()
	if newWl == nil || newWl.Obj.Name != "workload-2" {
		t.Error("failed to Pop workload")
	}
	if cq.Pop() != nil {
		t.Error("ClusterQueue should be empty")
	}
}

func Test_Delete(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl1 := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	wl2 := utiltesting.MakeWorkload("workload-2", defaultNamespace).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl1))
	cq.PushOrUpdate(workload.NewInfo(wl2))
	if cq.Pending() != 2 {
		t.Error("ClusterQueue should have two workload")
	}
	cq.Delete(wl1)
	if cq.Pending() != 1 {
		t.Error("ClusterQueue should have only one workload")
	}
	// Change workload item, ClusterQueue.Delete should only care about the namespace and name.
	wl2.Spec = kueue.WorkloadSpec{QueueName: "default"}
	cq.Delete(wl2)
	if cq.Pending() != 0 {
		t.Error("ClusterQueue should have be empty")
	}
}

func Test_Dump(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl1 := workload.NewInfo(utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj())
	wl2 := workload.NewInfo(utiltesting.MakeWorkload("workload-2", defaultNamespace).Obj())
	if _, ok := cq.Dump(); ok {
		t.Error("ClusterQueue should be empty")
	}
	cq.PushOrUpdate(wl1)
	cq.PushOrUpdate(wl2)
	if data, ok := cq.Dump(); !(ok && data.HasAll("workload-1", "workload-2")) {
		t.Error("dump data is not right")
	}
}

func Test_Info(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	if info := cq.Info(keyFunc(workload.NewInfo(wl))); info != nil {
		t.Error("workload doesn't exist")
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if info := cq.Info(keyFunc(workload.NewInfo(wl))); info == nil {
		t.Error("expected workload to exist")
	}
}

func Test_AddFromQueue(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	queue := &Queue{
		items: map[string]*workload.Info{
			wl.Name: workload.NewInfo(wl),
		},
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if added := cq.AddFromQueue(queue); added {
		t.Error("expected workload not to be added")
	}
	cq.Delete(wl)
	if added := cq.AddFromQueue(queue); !added {
		t.Error("workload should be added to the ClusterQueue")
	}
}

func Test_DeleteFromQueue(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl1 := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	wl2 := utiltesting.MakeWorkload("workload-2", defaultNamespace).Obj()
	queue := &Queue{
		items: map[string]*workload.Info{
			wl1.Name: workload.NewInfo(wl1),
			wl2.Name: workload.NewInfo(wl2),
		},
	}
	cq.AddFromQueue(queue)
	if cq.Pending() == 0 {
		t.Error("ClusterQueue should not be empty")
	}
	cq.DeleteFromQueue(queue)
	if cq.Pending() != 0 {
		t.Error("ClusterQueue should be empty")
	}
}

func Test_RequeueIfNotPresent(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), true); !ok {
		t.Error("failed to requeue nonexistent workload")
	}
	if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), true); ok {
		t.Error("existent workload shouldn't be added again")
	}
}
