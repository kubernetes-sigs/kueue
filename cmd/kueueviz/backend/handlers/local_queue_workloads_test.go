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

package handlers

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type fakeWorkloadClient struct {
	workloads []kueueapi.Workload
	err       error
}

func (f *fakeWorkloadClient) Get(_ context.Context, _ ctrlclient.ObjectKey, _ ctrlclient.Object, _ ...ctrlclient.GetOption) error {
	return f.err
}

func (f *fakeWorkloadClient) List(_ context.Context, list ctrlclient.ObjectList, _ ...ctrlclient.ListOption) error {
	if f.err != nil {
		return f.err
	}
	if wl, ok := list.(*kueueapi.WorkloadList); ok {
		wl.Items = f.workloads
	}
	return nil
}

func (f *fakeWorkloadClient) GetInformerForKind(_ context.Context, _ schema.GroupVersionKind, _ ...ctrlcache.InformerGetOption) (ctrlcache.Informer, error) {
	return nil, nil
}

func makeWorkload(name, namespace, queueName string) kueueapi.Workload {
	return kueueapi.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: kueueapi.WorkloadSpec{
			QueueName: kueueapi.LocalQueueName(queueName),
		},
	}
}

func TestFetchLocalQueueWorkloads(t *testing.T) {
	tests := map[string]struct {
		workloads []kueueapi.Workload
		listErr   error
		namespace string
		queueName string
		wantCount int
		wantErr   bool
	}{
		"returns only workloads matching the queue name": {
			workloads: []kueueapi.Workload{
				makeWorkload("wl-1", "ns-1", "queue-a"),
				makeWorkload("wl-2", "ns-1", "queue-b"),
				makeWorkload("wl-3", "ns-1", "queue-a"),
			},
			namespace: "ns-1",
			queueName: "queue-a",
			wantCount: 2,
		},
		"excludes workloads from other queues in the same namespace": {
			workloads: []kueueapi.Workload{
				makeWorkload("wl-1", "ns-1", "queue-b"),
				makeWorkload("wl-2", "ns-1", "queue-c"),
			},
			namespace: "ns-1",
			queueName: "queue-a",
			wantCount: 0,
		},
		"returns empty when namespace has no workloads": {
			workloads: []kueueapi.Workload{},
			namespace: "ns-1",
			queueName: "queue-a",
			wantCount: 0,
		},
		"propagates list error": {
			listErr: errors.New("list failed"),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := &Handlers{client: &fakeWorkloadClient{workloads: tc.workloads, err: tc.listErr}}

			got, err := h.fetchLocalQueueWorkloads(t.Context(), tc.namespace, tc.queueName)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			workloads, ok := got.([]any)
			if !ok {
				t.Fatalf("expected []any, got %T", got)
			}
			if len(workloads) != tc.wantCount {
				t.Fatalf("got %d workloads, want %d", len(workloads), tc.wantCount)
			}
		})
	}
}
