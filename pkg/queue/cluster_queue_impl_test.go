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
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
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

func Test_AddFromLocalQueue(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	queue := &LocalQueue{
		items: map[string]*workload.Info{
			wl.Name: workload.NewInfo(wl),
		},
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if added := cq.AddFromLocalQueue(queue); added {
		t.Error("expected workload not to be added")
	}
	cq.Delete(wl)
	if added := cq.AddFromLocalQueue(queue); !added {
		t.Error("workload should be added to the ClusterQueue")
	}
}

func Test_DeleteFromLocalQueue(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	q := utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj()
	qImpl := newLocalQueue(q)
	wl1 := utiltesting.MakeWorkload("wl1", "").Queue(q.Name).Obj()
	wl2 := utiltesting.MakeWorkload("wl2", "").Queue(q.Name).Obj()
	wl3 := utiltesting.MakeWorkload("wl3", "").Queue(q.Name).Obj()
	wl4 := utiltesting.MakeWorkload("wl4", "").Queue(q.Name).Obj()
	admissibleworkloads := []*kueue.Workload{wl1, wl2}
	inadmissibleWorkloads := []*kueue.Workload{wl3, wl4}

	for _, w := range admissibleworkloads {
		wInfo := workload.NewInfo(w)
		cq.PushOrUpdate(wInfo)
		qImpl.AddOrUpdate(wInfo)
	}

	for _, w := range inadmissibleWorkloads {
		wInfo := workload.NewInfo(w)
		cq.RequeueIfNotPresent(wInfo, RequeueReasonNamespaceMismatch)
		qImpl.AddOrUpdate(wInfo)
	}

	wantPending := len(admissibleworkloads) + len(inadmissibleWorkloads)
	if pending := cq.Pending(); pending != wantPending {
		t.Errorf("clusterQueue's workload number not right, want %v, got %v", wantPending, pending)
	}
	if len(cq.inadmissibleWorkloads) != len(inadmissibleWorkloads) {
		t.Errorf("clusterQueue's workload number in inadmissibleWorkloads not right, want %v, got %v", len(inadmissibleWorkloads), len(cq.inadmissibleWorkloads))
	}

	cq.DeleteFromLocalQueue(qImpl)
	if cq.Pending() != 0 {
		t.Error("clusterQueue should be empty")
	}
}

func Test_RequeueIfNotPresent(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), RequeueReasonGeneric); !ok {
		t.Error("failed to requeue nonexistent workload")
	}
	if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), RequeueReasonGeneric); ok {
		t.Error("existent workload shouldn't be added again")
	}
}

func TestClusterQueueImpl(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}
	clientBuilder := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1", Labels: map[string]string{"dep": "eng"}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns2", Labels: map[string]string{"dep": "sales"}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns3", Labels: map[string]string{"dep": "marketing"}}},
		)
	cl := clientBuilder.Build()

	var workloads = []*kueue.Workload{
		utiltesting.MakeWorkload("w1", "ns1").Queue("q1").Obj(),
		utiltesting.MakeWorkload("w2", "ns2").Queue("q2").Obj(),
		utiltesting.MakeWorkload("w3", "ns3").Queue("q3").Obj(),
	}
	var updatedWorkloads = make([]*kueue.Workload, len(workloads))

	updatedWorkloads[0] = workloads[0].DeepCopy()
	updatedWorkloads[0].Spec.QueueName = "q2"
	updatedWorkloads[1] = workloads[1].DeepCopy()
	updatedWorkloads[1].Spec.QueueName = "q1"

	tests := map[string]struct {
		workloadsToAdd                    []*kueue.Workload
		inadmissibleWorkloadsToRequeue    []*workload.Info
		admissibleWorkloadsToRequeue      []*workload.Info
		workloadsToUpdate                 []*kueue.Workload
		workloadsToDelete                 []*kueue.Workload
		queueInadmissibleWorkloads        bool
		wantActiveWorkloads               sets.String
		wantPending                       int
		wantInadmissibleWorkloadsRequeued bool
	}{
		"add, update, delete workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0], workloads[1]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[0]},
			workloadsToDelete:              []*kueue.Workload{workloads[0]},
			wantActiveWorkloads:            sets.NewString(workloads[1].Name),
			wantPending:                    1,
		},
		"re-queue inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			wantActiveWorkloads:            sets.NewString(workloads[0].Name),
			wantPending:                    2,
		},
		"re-queue admissible workload that was inadmissible": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			admissibleWorkloadsToRequeue:   []*workload.Info{workload.NewInfo(workloads[1])},
			wantActiveWorkloads:            sets.NewString(workloads[0].Name, workloads[1].Name),
			wantPending:                    2,
		},
		"re-queue inadmissible workload and flush": {
			workloadsToAdd:                    []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue:    []*workload.Info{workload.NewInfo(workloads[1])},
			queueInadmissibleWorkloads:        true,
			wantActiveWorkloads:               sets.NewString(workloads[0].Name, workloads[1].Name),
			wantPending:                       2,
			wantInadmissibleWorkloadsRequeued: true,
		},
		"avoid re-queueing inadmissible workloads not matching namespace selector": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[2])},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            sets.NewString(workloads[0].Name),
			wantPending:                    2,
		},
		"update inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[1]},
			wantActiveWorkloads:            sets.NewString(workloads[0].Name, workloads[1].Name),
			wantPending:                    2,
		},
		"delete inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToDelete:              []*kueue.Workload{workloads[1]},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            sets.NewString(workloads[0].Name),
			wantPending:                    1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cq := newClusterQueueImpl(keyFunc, byCreationTime)

			err := cq.Update(utiltesting.MakeClusterQueue("cq").
				NamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng", "sales"},
						},
					},
				}).Obj())
			if err != nil {
				t.Fatalf("Failed updating clusterQueue: %v", err)
			}

			for _, w := range test.workloadsToAdd {
				cq.PushOrUpdate(workload.NewInfo(w))
			}

			for _, w := range test.inadmissibleWorkloadsToRequeue {
				cq.RequeueIfNotPresent(w, RequeueReasonNamespaceMismatch)
			}
			for _, w := range test.admissibleWorkloadsToRequeue {
				cq.RequeueIfNotPresent(w, RequeueReasonGeneric)
			}

			for _, w := range test.workloadsToUpdate {
				cq.PushOrUpdate(workload.NewInfo(w))
			}

			for _, w := range test.workloadsToDelete {
				cq.Delete(w)
			}

			if test.queueInadmissibleWorkloads {
				if diff := cmp.Diff(test.wantInadmissibleWorkloadsRequeued,
					cq.QueueInadmissibleWorkloads(context.Background(), cl)); diff != "" {
					t.Errorf("Unexpected requeueing of inadmissible workloads (-want,+got):\n%s", diff)
				}
			}

			gotWorkloads, _ := cq.Dump()
			if diff := cmp.Diff(test.wantActiveWorkloads, gotWorkloads); diff != "" {
				t.Errorf("Unexpected items in cluster foo (-want,+got):\n%s", diff)
			}
			if got := cq.Pending(); got != test.wantPending {
				t.Errorf("Got %d pending workloads, want %d", got, test.wantPending)
			}
		})
	}
}

func TestQueueInadmissibleWorkloadsDuringScheduling(t *testing.T) {
	cq := newClusterQueueImpl(keyFunc, byCreationTime)
	cq.namespaceSelector = labels.Everything()
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	scheme := utiltesting.MustGetScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		wl,
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: defaultNamespace},
		},
	).Build()
	ctx := context.Background()
	cq.PushOrUpdate(workload.NewInfo(wl))

	wantActiveWorkloads := sets.NewString("workload-1")

	activeWorkloads, _ := cq.Dump()
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads); diff != "" {
		t.Errorf("Unexpected active workloads before events (-want,+got):\n%s", diff)
	}

	// Simulate requeueing during scheduling attempt.
	head := cq.Pop()
	cq.QueueInadmissibleWorkloads(ctx, cl)
	cq.requeueIfNotPresent(head, false)

	activeWorkloads, _ = cq.Dump()
	wantActiveWorkloads = sets.NewString("workload-1")
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads); diff != "" {
		t.Errorf("Unexpected active workloads after events (-want,+got):\n%s", diff)
	}
}
