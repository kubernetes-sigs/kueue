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

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func makeClusterQueue(name, cohort string, flavors ...string) *kueueapi.ClusterQueue {
	var fq []kueueapi.FlavorQuotas
	for _, f := range flavors {
		fq = append(fq, kueueapi.FlavorQuotas{Name: kueueapi.ResourceFlavorReference(f)})
	}
	cq := &kueueapi.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kueueapi.ClusterQueueSpec{
			CohortName: kueueapi.CohortReference(cohort),
		},
	}
	if len(fq) > 0 {
		cq.Spec.ResourceGroups = []kueueapi.ResourceGroup{{Flavors: fq}}
	}
	return cq
}

func makeLocalQueue(namespace, name, clusterQueue string) *kueueapi.LocalQueue {
	return &kueueapi.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: kueueapi.LocalQueueSpec{
			ClusterQueue: kueueapi.ClusterQueueReference(clusterQueue),
		},
	}
}

func listErrorFuncs(err error) *interceptor.Funcs {
	return &interceptor.Funcs{
		List: func(_ context.Context, _ ctrlclient.WithWatch, _ ctrlclient.ObjectList, _ ...ctrlclient.ListOption) error {
			return err
		},
	}
}

func TestFetchClusterQueues(t *testing.T) {
	tests := map[string]struct {
		objs      []ctrlclient.Object
		funcs     *interceptor.Funcs
		wantNames []string
		wantErr   bool
	}{
		"no cluster queues returns nil": {},
		"returns every cluster queue": {
			objs:      []ctrlclient.Object{makeClusterQueue("cq-a", "team"), makeClusterQueue("cq-b", "")},
			wantNames: []string{"cq-a", "cq-b"},
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchClusterQueues(t.Context())
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchClusterQueues() error = %v, wantErr %v", err, tc.wantErr)
			}

			var gotNames []string
			for _, cq := range got {
				gotNames = append(gotNames, cq["name"].(string))
			}
			if diff := cmp.Diff(tc.wantNames, gotNames); diff != "" {
				t.Errorf("unexpected cluster queue names (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFetchClusterQueuesFields(t *testing.T) {
	cq := makeClusterQueue("cq-a", "team", "on-demand", "spot")
	cq.Status.AdmittedWorkloads = 2
	cq.Status.PendingWorkloads = 3
	cq.Status.ReservingWorkloads = 1

	h := New(newFakeClient(t, nil, cq), nil)
	got, err := h.fetchClusterQueues(t.Context())
	if err != nil {
		t.Fatalf("fetchClusterQueues() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d cluster queues, want 1", len(got))
	}

	item := got[0]
	if item["cohort"] != "team" {
		t.Errorf("cohort = %v, want %q", item["cohort"], "team")
	}
	if diff := cmp.Diff([]string{"on-demand", "spot"}, item["flavors"]); diff != "" {
		t.Errorf("unexpected flavors (-want,+got):\n%s", diff)
	}
	if item["admittedWorkloads"] != int32(2) || item["pendingWorkloads"] != int32(3) || item["reservingWorkloads"] != int32(1) {
		t.Errorf("workload counts = %v/%v/%v, want 2/3/1",
			item["admittedWorkloads"], item["pendingWorkloads"], item["reservingWorkloads"])
	}
}

func TestFetchClusterQueueDetails(t *testing.T) {
	objs := []ctrlclient.Object{
		makeClusterQueue("cq-a", "team", "on-demand"),
		makeLocalQueue("ns-1", "lq-1", "cq-a"),
		makeLocalQueue("ns-1", "lq-2", "cq-a"),
		makeLocalQueue("ns-2", "lq-3", "cq-a"),
		makeLocalQueue("ns-3", "lq-other", "cq-b"),
	}

	tests := map[string]struct {
		objs       []ctrlclient.Object
		funcs      *interceptor.Funcs
		queueName  string
		wantQueues []string
		wantErr    bool
	}{
		"lists every local queue of the cluster queue": {
			objs:       objs,
			queueName:  "cq-a",
			wantQueues: []string{"ns-1/lq-1", "ns-1/lq-2", "ns-2/lq-3"},
		},
		"cluster queue without local queues returns empty list": {
			objs:       []ctrlclient.Object{makeClusterQueue("cq-a", "")},
			queueName:  "cq-a",
			wantQueues: []string{},
		},
		"missing cluster queue returns error": {
			queueName: "missing",
			wantErr:   true,
		},
		"local queue list error is returned": {
			objs:      objs,
			funcs:     listErrorFuncs(errors.New("boom")),
			queueName: "cq-a",
			wantErr:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchClusterQueueDetails(t.Context(), tc.queueName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchClusterQueueDetails() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			result := got.(map[string]any)
			gotQueues := []string{}
			for _, q := range result["queues"].([]map[string]any) {
				gotQueues = append(gotQueues, q["namespace"].(string)+"/"+q["name"].(string))
			}
			if diff := cmp.Diff(tc.wantQueues, gotQueues); diff != "" {
				t.Errorf("unexpected queues (-want,+got):\n%s", diff)
			}
		})
	}
}
