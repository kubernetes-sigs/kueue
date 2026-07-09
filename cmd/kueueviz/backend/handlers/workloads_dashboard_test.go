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
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type fakeDashboardClient struct {
	workloads []kueueapi.Workload
	pods      []corev1.Pod

	podListCallsByNamespace map[string]int
}

func (d *fakeDashboardClient) Get(_ context.Context, _ ctrlclient.ObjectKey, _ ctrlclient.Object, _ ...ctrlclient.GetOption) error {
	return nil
}

func (d *fakeDashboardClient) List(_ context.Context, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
	listOpts := &ctrlclient.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(listOpts)
	}

	switch l := list.(type) {
	case *kueueapi.WorkloadList:
		for _, workload := range d.workloads {
			if listOpts.Namespace == "" || workload.Namespace == listOpts.Namespace {
				l.Items = append(l.Items, workload)
			}
		}
	case *corev1.PodList:
		if d.podListCallsByNamespace == nil {
			d.podListCallsByNamespace = make(map[string]int)
		}
		d.podListCallsByNamespace[listOpts.Namespace]++
		for _, pod := range d.pods {
			if listOpts.Namespace == "" || pod.Namespace == listOpts.Namespace {
				l.Items = append(l.Items, pod)
			}
		}
	default:
		return fmt.Errorf("unexpected list type %T", list)
	}

	return nil
}

func (d *fakeDashboardClient) GetInformerForKind(_ context.Context, _ schema.GroupVersionKind, _ ...ctrlcache.InformerGetOption) (ctrlcache.Informer, error) {
	return nil, nil
}

func TestFetchWorkloadsDashboardDataDoesNotListPodsPerWorkload(t *testing.T) {
	client := &fakeDashboardClient{
		workloads: []kueueapi.Workload{
			makeDashboardWorkload("wl-1", "ns-1", "wl-uid-1", "job-uid-1"),
			makeDashboardWorkload("wl-2", "ns-1", "wl-uid-2", "job-uid-2"),
		},
		pods: []corev1.Pod{
			makeDashboardPod("pod-1", "ns-1", "job-uid-1"),
			makeDashboardPod("pod-2", "ns-1", "job-uid-2"),
		},
	}
	h := &Handlers{client: client}

	if _, err := h.fetchWorkloadsDashboardData(t.Context(), "ns-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// This catches the O(workloads*pods) regression: with two workloads in ns-1,
	// the old implementation listed pods once per workload and would count 2 here.
	if got := client.podListCallsByNamespace["ns-1"]; got != 1 {
		t.Fatalf("pod list calls for ns-1 = %d, want 1", got)
	}
	if got := len(client.podListCallsByNamespace); got != 1 {
		t.Fatalf("pod list namespaces = %d, want 1", got)
	}
}

func TestFetchWorkloadsDashboardDataKeepsPodsNamespaceScoped(t *testing.T) {
	client := &fakeDashboardClient{
		workloads: []kueueapi.Workload{
			makeDashboardWorkload("wl-1", "ns-1", "wl-uid-1", "job-uid-1"),
			makeDashboardWorkload("wl-2", "ns-2", "wl-uid-2", "job-uid-2"),
		},
		pods: []corev1.Pod{
			makeDashboardPod("pod-1", "ns-1", "job-uid-1"),
			makeDashboardPod("pod-2", "ns-2", "job-uid-1"),
			makeDashboardPod("pod-3", "ns-2", "job-uid-2"),
		},
	}
	h := &Handlers{client: client}

	got, err := h.fetchWorkloadsDashboardData(t.Context(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := client.podListCallsByNamespace["ns-1"]; got != 1 {
		t.Fatalf("pod list calls for ns-1 = %d, want 1", got)
	}
	if got := client.podListCallsByNamespace["ns-2"]; got != 1 {
		t.Fatalf("pod list calls for ns-2 = %d, want 1", got)
	}

	items := dashboardWorkloadItems(t, got)
	wantPodCounts := map[string]int{
		"wl-1": 1,
		"wl-2": 1,
	}
	for _, item := range items {
		if got, want := len(item.Pods), wantPodCounts[item.Name]; got != want {
			t.Fatalf("workload %s has %d pods, want %d", item.Name, got, want)
		}
	}
}

func dashboardWorkloadItems(t *testing.T, got any) []workloadResult {
	t.Helper()

	workloadsResult, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", got)
	}
	items, ok := workloadsResult["items"].([]workloadResult)
	if !ok {
		t.Fatalf("expected []workloadResult, got %T", workloadsResult["items"])
	}
	return items
}

func makeDashboardWorkload(name, namespace, uid, jobUID string) kueueapi.Workload {
	return kueueapi.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
			Labels: map[string]string{
				"kueue.x-k8s.io/job-uid": jobUID,
			},
		},
	}
}

func makeDashboardPod(name, namespace, controllerUID string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"controller-uid": controllerUID,
			},
		},
	}
}
