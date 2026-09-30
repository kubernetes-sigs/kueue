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
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func makeWorkloadObj(namespace, name, queueName string, conditions ...metav1.Condition) *kueueapi.Workload {
	return &kueueapi.Workload{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       kueueapi.WorkloadSpec{QueueName: kueueapi.LocalQueueName(queueName)},
		Status:     kueueapi.WorkloadStatus{Conditions: conditions},
	}
}

func TestFetchWorkloads(t *testing.T) {
	objs := []ctrlclient.Object{
		makeWorkloadObj("ns-1", "wl-1", "lq-1"),
		makeWorkloadObj("ns-2", "wl-2", "lq-1"),
	}

	tests := map[string]struct {
		funcs     *interceptor.Funcs
		namespace string
		want      []string
		wantErr   bool
	}{
		"empty namespace lists all workloads": {
			want: []string{"ns-1/wl-1", "ns-2/wl-2"},
		},
		"namespace filters workloads": {
			namespace: "ns-2",
			want:      []string{"ns-2/wl-2"},
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, objs...), nil)

			got, err := h.fetchWorkloads(t.Context(), tc.namespace)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchWorkloads() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			var gotNames []string
			for _, wl := range got.(*kueueapi.WorkloadList).Items {
				gotNames = append(gotNames, wl.Namespace+"/"+wl.Name)
			}
			if diff := cmp.Diff(tc.want, gotNames); diff != "" {
				t.Errorf("unexpected workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFetchWorkloadDetails(t *testing.T) {
	preempted := metav1.Condition{
		Type:   kueueapi.WorkloadPreempted,
		Status: metav1.ConditionTrue,
		Reason: "InClusterQueue",
	}

	tests := map[string]struct {
		objs                 []ctrlclient.Object
		workloadName         string
		wantClusterQueueName string
		wantPreemption       map[string]any
		wantErr              bool
	}{
		"cluster queue name comes from the local queue": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1"),
				makeLocalQueue("ns-1", "lq-1", "cq-a"),
			},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-a",
			wantPreemption:       map[string]any{},
		},
		"preemption condition is reported": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1", preempted),
				makeLocalQueue("ns-1", "lq-1", "cq-a"),
			},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-a",
			wantPreemption:       map[string]any{"preempted": true, "reason": "InClusterQueue"},
		},
		"workload without queue name has unknown cluster queue": {
			objs:                 []ctrlclient.Object{makeWorkloadObj("ns-1", "wl-1", "")},
			workloadName:         "wl-1",
			wantClusterQueueName: "Unknown",
			wantPreemption:       map[string]any{},
		},
		"local queue without cluster queue has unknown cluster queue": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1"),
				makeLocalQueue("ns-1", "lq-1", ""),
			},
			workloadName:         "wl-1",
			wantClusterQueueName: "Unknown",
			wantPreemption:       map[string]any{},
		},
		"missing local queue returns error": {
			objs:         []ctrlclient.Object{makeWorkloadObj("ns-1", "wl-1", "lq-1")},
			workloadName: "wl-1",
			wantErr:      true,
		},
		"missing workload returns error": {
			workloadName: "missing",
			wantErr:      true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, nil, tc.objs...), nil)

			got, err := h.fetchWorkloadDetails(t.Context(), "ns-1", tc.workloadName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchWorkloadDetails() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			// Check the JSON the frontend receives: the workload object
			// plus the clusterQueueName and preemption fields.
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var body struct {
				Metadata         metav1.ObjectMeta `json:"metadata"`
				ClusterQueueName string            `json:"clusterQueueName"`
				Preemption       map[string]any    `json:"preemption"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if body.Metadata.Name != tc.workloadName {
				t.Errorf("metadata.name = %q, want %q", body.Metadata.Name, tc.workloadName)
			}
			if body.ClusterQueueName != tc.wantClusterQueueName {
				t.Errorf("clusterQueueName = %q, want %q", body.ClusterQueueName, tc.wantClusterQueueName)
			}
			if diff := cmp.Diff(tc.wantPreemption, body.Preemption); diff != "" {
				t.Errorf("unexpected preemption (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFetchWorkloadEvents(t *testing.T) {
	event := corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "ns-1", Name: "wl-1.123"},
		InvolvedObject: corev1.ObjectReference{Name: "wl-1"},
		Reason:         "QuotaReserved",
	}

	tests := map[string]struct {
		listErr error
		want    []corev1.Event
		wantErr bool
	}{
		"returns events for the workload": {
			want: []corev1.Event{event},
		},
		"list error is returned": {
			listErr: errors.New("boom"),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gotOpts ctrlclient.ListOptions
			funcs := &interceptor.Funcs{
				List: func(_ context.Context, _ ctrlclient.WithWatch, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
					gotOpts.ApplyOptions(opts)
					if tc.listErr != nil {
						return tc.listErr
					}
					// The fake client needs an index for field selectors,
					// so return the event directly and check the options.
					list.(*corev1.EventList).Items = []corev1.Event{event}
					return nil
				},
			}
			h := New(newFakeClient(t, funcs), nil)

			got, err := h.fetchWorkloadEvents(t.Context(), "ns-1", "wl-1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchWorkloadEvents() error = %v, wantErr %v", err, tc.wantErr)
			}

			if gotOpts.Namespace != "ns-1" {
				t.Errorf("namespace = %q, want %q", gotOpts.Namespace, "ns-1")
			}
			if gotOpts.FieldSelector == nil || gotOpts.FieldSelector.String() != "involvedObject.name=wl-1" {
				t.Errorf("field selector = %v, want %q", gotOpts.FieldSelector, "involvedObject.name=wl-1")
			}
			if tc.wantErr {
				return
			}
			if diff := cmp.Diff(tc.want, got.([]corev1.Event)); diff != "" {
				t.Errorf("unexpected events (-want,+got):\n%s", diff)
			}
		})
	}
}
