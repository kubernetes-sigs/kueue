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
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"kueueviz/middleware"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func makeResourceFlavor(name string, nodeLabels map[string]string) *kueueapi.ResourceFlavor {
	return &kueueapi.ResourceFlavor{
		Name: name,
		Spec: kueueapi.ResourceFlavorSpec{NodeLabels: nodeLabels},
	}
}

func makeNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		Name: name, Labels: labels,
	}
}

func TestFetchResourceFlavors(t *testing.T) {
	tests := map[string]struct {
		objs      []ctrlclient.Object
		funcs     *interceptor.Funcs
		wantNames []string
		wantErr   bool
	}{
		"no flavors returns nil": {},
		"returns every flavor": {
			objs: []ctrlclient.Object{
				makeResourceFlavor("on-demand", nil),
				makeResourceFlavor("spot", map[string]string{"capacity": "spot"}),
			},
			wantNames: []string{"on-demand", "spot"},
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil, nil)

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
				gotNames = append(gotNames, item["name"].(string))
				if _, ok := item["details"]; !ok {
					t.Errorf("flavor %v has no details field", item["name"])
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
		objs        []ctrlclient.Object
		funcs       *interceptor.Funcs
		authorizer  middleware.Authorizer
		flavorName  string
		wantQueues  []map[string]any
		wantNodes   []string
		wantOmitted []string
		wantErr     bool
	}{
		"returns queues and nodes using the flavor": {
			objs:       objs,
			flavorName: "spot",
			wantQueues: []map[string]any{{
				"queueName": "cq-a",
				"quota":     []map[string]any{{"resource": "cpu", "nominalQuota": "4"}},
			}},
			wantNodes:   []string{"node-spot"},
			wantOmitted: []string{},
		},
		"denied access omits queues and nodes": {
			objs:       objs,
			flavorName: "spot",
			authorizer: authorizerFunc(func(authorizationv1.ResourceAttributes) (bool, error) {
				return false, nil
			}),
			wantQueues:  []map[string]any{},
			wantOmitted: []string{"queues", "nodes"},
		},
		"only node access denied omits nodes": {
			objs:       objs,
			flavorName: "spot",
			authorizer: authorizerFunc(func(attrs authorizationv1.ResourceAttributes) (bool, error) {
				return attrs.Resource != NodesGVR().Resource, nil
			}),
			wantQueues: []map[string]any{{
				"queueName": "cq-a",
				"quota":     []map[string]any{{"resource": "cpu", "nominalQuota": "4"}},
			}},
			wantOmitted: []string{"nodes"},
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
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil, tc.authorizer)

			got, err := h.fetchResourceFlavorDetails(t.Context(), tc.flavorName, middleware.Identity{Username: "alice"})
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
			if diff := cmp.Diff(tc.wantOmitted, got["omittedPanels"]); diff != "" {
				t.Errorf("unexpected omittedPanels (-want,+got):\n%s", diff)
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
