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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func makeResourceFlavor(name string, nodeLabels map[string]string) *kueueapi.ResourceFlavor {
	return &kueueapi.ResourceFlavor{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kueueapi.ResourceFlavorSpec{NodeLabels: nodeLabels},
	}
}

func makeNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func TestFetchResourceFlavors(t *testing.T) {
	tests := map[string]struct {
		objs           []ctrlclient.Object
		funcs          *interceptor.Funcs
		wantNames      []string
		wantNodeLabels map[string]map[string]any
		wantErr        bool
	}{
		"no flavors returns nil": {},
		"returns every flavor": {
			objs: []ctrlclient.Object{
				makeResourceFlavor("on-demand", nil),
				makeResourceFlavor("spot", map[string]string{"capacity": "spot"}),
			},
			wantNames: []string{"on-demand", "spot"},
			wantNodeLabels: map[string]map[string]any{
				"on-demand": nil,
				"spot":      {"capacity": "spot"},
			},
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchResourceFlavors(t.Context())
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchResourceFlavors() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			// The result is a slice of an unexported struct, so read it
			// through JSON field names the frontend relies on.
			var gotNames []string
			for _, item := range toJSONList(t, got) {
				name := item["name"].(string)
				gotNames = append(gotNames, name)

				// details carries the flavor spec, so its nodeLabels must match.
				details, ok := item["details"].(map[string]any)
				if !ok {
					t.Errorf("flavor %s has no details object", name)
					continue
				}
				gotNodeLabels, _ := details["nodeLabels"].(map[string]any)
				if diff := cmp.Diff(tc.wantNodeLabels[name], gotNodeLabels); diff != "" {
					t.Errorf("unexpected details.nodeLabels for flavor %s (-want,+got):\n%s", name, diff)
				}
			}
			if diff := cmp.Diff(tc.wantNames, gotNames); diff != "" {
				t.Errorf("unexpected flavor names (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFetchResourceFlavorDetails(t *testing.T) {
	cqA := makeClusterQueue("cq-a", "", "spot")
	cqA.Spec.ResourceGroups[0].CoveredResources = []corev1.ResourceName{corev1.ResourceCPU}
	cqA.Spec.ResourceGroups[0].Flavors[0].Resources = []kueueapi.ResourceQuota{
		{Name: corev1.ResourceCPU, NominalQuota: resource.MustParse("4")},
	}

	objs := []ctrlclient.Object{
		makeResourceFlavor("spot", map[string]string{"capacity": "spot"}),
		cqA,
		makeClusterQueue("cq-b", "", "on-demand"),
		makeNode("node-spot", map[string]string{"capacity": "spot", "zone": "a"}),
		makeNode("node-on-demand", map[string]string{"capacity": "on-demand"}),
	}

	tests := map[string]struct {
		objs       []ctrlclient.Object
		funcs      *interceptor.Funcs
		flavorName string
		wantQueues []map[string]any
		wantNodes  []string
		wantErr    bool
	}{
		"returns queues and nodes using the flavor": {
			objs:       objs,
			flavorName: "spot",
			wantQueues: []map[string]any{{
				"queueName": "cq-a",
				"quota":     []map[string]any{{"resource": "cpu", "nominalQuota": "4"}},
			}},
			wantNodes: []string{"node-spot"},
		},
		"missing flavor returns error": {
			flavorName: "missing",
			wantErr:    true,
		},
		"node list error is returned": {
			objs:       objs,
			flavorName: "spot",
			funcs: &interceptor.Funcs{
				List: func(ctx context.Context, c ctrlclient.WithWatch, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
					if _, ok := list.(*corev1.NodeList); ok {
						return errors.New("boom")
					}
					return c.List(ctx, list, opts...)
				},
			},
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchResourceFlavorDetails(t.Context(), tc.flavorName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchResourceFlavorDetails() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			if diff := cmp.Diff(tc.wantQueues, got["queues"]); diff != "" {
				t.Errorf("unexpected queues (-want,+got):\n%s", diff)
			}
			var gotNodes []string
			for _, n := range got["nodes"].([]map[string]any) {
				gotNodes = append(gotNodes, n["name"].(string))
			}
			if diff := cmp.Diff(tc.wantNodes, gotNodes); diff != "" {
				t.Errorf("unexpected nodes (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestHasMatchingLabels(t *testing.T) {
	tests := map[string]struct {
		nodeLabels   map[string]string
		flavorLabels map[string]string
		want         bool
	}{
		"flavor without labels matches any node": {
			nodeLabels: map[string]string{"zone": "a"},
			want:       true,
		},
		"all flavor labels present": {
			nodeLabels:   map[string]string{"zone": "a", "capacity": "spot"},
			flavorLabels: map[string]string{"capacity": "spot"},
			want:         true,
		},
		"label value differs": {
			nodeLabels:   map[string]string{"capacity": "on-demand"},
			flavorLabels: map[string]string{"capacity": "spot"},
		},
		"label missing on node": {
			nodeLabels:   map[string]string{"zone": "a"},
			flavorLabels: map[string]string{"capacity": "spot"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := hasMatchingLabels(tc.nodeLabels, tc.flavorLabels); got != tc.want {
				t.Errorf("hasMatchingLabels() = %v, want %v", got, tc.want)
			}
		})
	}
}

// toJSONList round-trips v through JSON so tests can assert on the field
// names the frontend receives.
func toJSONList(t *testing.T, v any) []map[string]any {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestFetchResourceFlavorDetailsSpec(t *testing.T) {
	flavor := makeResourceFlavor("spot", map[string]string{"capacity": "spot"})
	flavor.Spec.NodeTaints = []corev1.Taint{{Key: "spot", Value: "true", Effect: corev1.TaintEffectNoSchedule}}
	flavor.Spec.Tolerations = []corev1.Toleration{{Key: "spot", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}

	h := New(newFakeClient(t, nil, flavor), nil, nil)
	got, err := h.fetchResourceFlavorDetails(t.Context(), "spot", middleware.Identity{Username: "alice"})
	if err != nil {
		t.Fatalf("fetchResourceFlavorDetails() error = %v", err)
	}

	// The ResourceFlavor detail page reads these keys from details.
	want := map[string]any{
		"nodeLabels":  flavor.Spec.NodeLabels,
		"nodeTaints":  flavor.Spec.NodeTaints,
		"tolerations": flavor.Spec.Tolerations,
	}
	if diff := cmp.Diff(want, got["details"]); diff != "" {
		t.Errorf("unexpected details (-want,+got):\n%s", diff)
	}
}
