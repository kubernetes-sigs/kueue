/*
Copyright 2024 The Kubernetes Authors.

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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// Event is a simplified representation of a Kubernetes event for testing purposes.
type Event struct {
	EventType string
	Reason    string
	Message   string
}

func TestDefaultLocalQueueReconciler(t *testing.T) {
	baseWorkload := utiltestingapi.MakeWorkload("workload", "ns").Queue("lq").Obj()

	cases := map[string]struct {
		clusterQueues       []*kueue.ClusterQueue
		namespaces          []*corev1.Namespace
		localQueues         []*kueue.LocalQueue // Existing LocalQueues to be set up before reconciliation
		globalNsSelectorStr string
		req                 reconcile.Request
		wantError           error
		wantLQs             []kueue.LocalQueue
		wantEvents          []Event
	}{
		"should create a default LocalQueue if the namespace matches cq and global selector is nil": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				cq.Spec.DefaultLocalQueue = &kueue.DefaultLocalQueue{Name: "default-lq"}
				return []*kueue.ClusterQueue{cq}
			}(),
			            namespaces: []*corev1.Namespace{
			                {
			                    ObjectMeta: metav1.ObjectMeta{
			                        Name:   "ns",
			                        Labels: map[string]string{"dep": "eng"},
			                    },
			                },
			            },
			            req: reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			            wantLQs: []kueue.LocalQueue{
			                *utiltestingapi.MakeLocalQueue("default-lq", "ns").
			                    ClusterQueue("cq").
			                    Labels(map[string]string{"kueue.x-k8s.io/auto-generated": "true"}).
			                    Annotations(map[string]string{"kueue.x-k8s.io/created-by-clusterqueue": "cq"}).
			                    Obj(),
			            },			wantEvents: []Event{
				{
					EventType: corev1.EventTypeNormal,
					Reason:    "LocalQueueCreated",
					Message:   "Created LocalQueue default-lq in namespace ns",
				},
			},
		},
		"should create a default LocalQueue if the namespace matches both cq and global selectors": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				cq.Spec.DefaultLocalQueue = &kueue.DefaultLocalQueue{Name: "default-lq"}
				return []*kueue.ClusterQueue{cq}
			}(),
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "ns",
						Labels: map[string]string{"dep": "eng", "team": "alpha"},
					},
				},
			},
			globalNsSelectorStr: "team=alpha",
			req:                 reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			wantLQs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("default-lq", "ns").
					ClusterQueue("cq").
					Labels(map[string]string{"kueue.x-k8s.io/auto-generated": "true"}).
					Annotations(map[string]string{"kueue.x-k8s.io/created-by-clusterqueue": "cq"}).
					Obj(),
			},
			wantEvents: []Event{
				{
					EventType: corev1.EventTypeNormal,
					Reason:    "LocalQueueCreated",
					Message:   "Created LocalQueue default-lq in namespace ns",
				},
			},
		},
		"should emit a warning event if a LocalQueue with the same name already exists without annotation": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				cq.Spec.DefaultLocalQueue = &kueue.DefaultLocalQueue{Name: "default-lq"}
				return []*kueue.ClusterQueue{cq}
			}(),
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "ns",
						Labels: map[string]string{"dep": "eng"},
					},
				},
			},
			localQueues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("default-lq", "ns").Obj(),
			},
			req: reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			wantLQs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("default-lq", "ns").Obj(),
			},
			wantEvents: []Event{
				{
					EventType: corev1.EventTypeWarning,
					Reason:    "DefaultLocalQueueExists",
					Message:   "Skipping LocalQueue creation in namespace ns, a LocalQueue with name default-lq already exists",
				},
			},
		},
		"should do nothing if defaultLocalQueue is not configured": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				return []*kueue.ClusterQueue{cq}
			}(),
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "ns",
						Labels: map[string]string{"dep": "eng"},
					},
				},
			},
			req:        reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			wantLQs:    []kueue.LocalQueue{},
			wantEvents: nil,
		},
		"should do nothing if clusterQueue is being deleted": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").DeletionTimestamp(time.Now()).Obj()
				cq.Finalizers = []string{"finalizer"}
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				cq.Spec.DefaultLocalQueue = &kueue.DefaultLocalQueue{Name: "default-lq"}
				return []*kueue.ClusterQueue{cq}
			}(),
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "ns",
						Labels: map[string]string{"dep": "eng"},
					},
				},
			},
			req:        reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			wantLQs:    []kueue.LocalQueue{},
			wantEvents: nil,
		},
		"should do nothing if no namespace matches": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				cq.Spec.DefaultLocalQueue = &kueue.DefaultLocalQueue{Name: "default-lq"}
				return []*kueue.ClusterQueue{cq}
			}(),
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "ns",
						Labels: map[string]string{"dep": "another"},
					},
				},
			},
			req:        reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			wantLQs:    []kueue.LocalQueue{},
			wantEvents: nil,
		},
		"should do nothing if a LocalQueue with the same name and annotation already exists": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				cq.Spec.DefaultLocalQueue = &kueue.DefaultLocalQueue{Name: "default-lq"}
				return []*kueue.ClusterQueue{cq}
			}(),
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "ns",
						Labels: map[string]string{"dep": "eng"},
					},
				},
			},
			localQueues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("default-lq", "ns").
					Annotations(map[string]string{"kueue.x-k8s.io/created-by-clusterqueue": "cq"}).
					Obj(),
			},
			req: reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			wantLQs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("default-lq", "ns").
					Annotations(map[string]string{"kueue.x-k8s.io/created-by-clusterqueue": "cq"}).
					Obj(),
			},
			wantEvents: nil,
		},
		"should not create a default LocalQueue if the namespace doesn't match the global selector": {
			clusterQueues: func() []*kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"dep": "eng"}}
				cq.Spec.DefaultLocalQueue = &kueue.DefaultLocalQueue{Name: "default-lq"}
				return []*kueue.ClusterQueue{cq}
			}(),
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "ns",
						Labels: map[string]string{"dep": "eng"},
					},
				},
			},
			globalNsSelectorStr: "team=alpha",
			req:                 reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq"}},
			wantLQs:             []kueue.LocalQueue{},
			wantEvents:          nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			builder.WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*baseWorkload}})

			for _, cq := range tc.clusterQueues {
				builder.WithObjects(cq)
			}
			for _, ns := range tc.namespaces {
				builder.WithObjects(ns)
			}
			for _, lq := range tc.localQueues {
				builder.WithObjects(lq)
			}

			k8sclient := builder.Build()
			recorder := record.NewFakeRecorder(10)

			var opts []DefaultLocalQueueReconcilerOption
			if tc.globalNsSelectorStr != "" {
				selector, err := labels.Parse(tc.globalNsSelectorStr)
				if err != nil {
					t.Fatalf("failed to parse globalNsSelector: %v", err)
				}
				opts = append(opts, WithNamespaceSelector(selector))
			}

			reconciler := NewDefaultLocalQueueReconciler(
				k8sclient,
				nil,
				nil,
				recorder,
				opts...,
			)

			_, gotErr := reconciler.Reconcile(ctx, tc.req)
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile() error (-want +got):\n%s", diff)
			}

			var lqList kueue.LocalQueueList
			if err := k8sclient.List(ctx, &lqList); err != nil {
				t.Errorf("Failed to list LocalQueues: %v", err)
			}

			if diff := cmp.Diff(tc.wantLQs, lqList.Items, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); diff != "" {
				t.Errorf("LocalQueues (-want +got):\n%s", diff)
			}

			close(recorder.Events)
			var gotEvents []Event
			for event := range recorder.Events {
				e := Event{}
				parts := strings.SplitN(event, " ", 3)
				if len(parts) < 2 {
					t.Fatalf("Invalid event format: %q", event)
				}
				e.EventType = parts[0]
				e.Reason = parts[1]
				if len(parts) > 2 {
					e.Message = strings.Trim(parts[2], "`")
				}
				gotEvents = append(gotEvents, e)
			}

			if diff := cmp.Diff(tc.wantEvents, gotEvents); diff != "" {
				t.Errorf("Events (-want +got):\n%s", diff)
			}
		})
	}
}

func getClientBuilder() (*fake.ClientBuilder, context.Context) {
	return utiltesting.NewClientBuilder(), context.Background()
}
