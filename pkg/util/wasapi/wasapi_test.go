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

package wasapi

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	podGroupGVK = schema.GroupVersionKind{Group: GroupName, Version: "v1alpha2", Kind: PodGroupKind}
	workloadGVK = schema.GroupVersionKind{Group: GroupName, Version: "v1alpha2", Kind: WorkloadKind}
)

// restMapperFor returns a RESTMapper that knows about the given WAS kinds at
// the given version, mimicking what a real cluster's discovery-backed
// RESTMapper would report.
func restMapperFor(gvks ...schema.GroupVersionKind) apimeta.RESTMapper {
	gvs := make([]schema.GroupVersion, 0, len(gvks))
	for _, gvk := range gvks {
		gvs = append(gvs, gvk.GroupVersion())
	}
	mapper := apimeta.NewDefaultRESTMapper(gvs)
	for _, gvk := range gvks {
		mapper.Add(gvk, apimeta.RESTScopeNamespace)
	}
	return mapper
}

func newFakeClient(mapper apimeta.RESTMapper, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithRESTMapper(mapper).WithObjects(objs...).Build()
}

func podGroup(namespace, name string, minCount *int64) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
		"spec": map[string]any{},
	}}
	if minCount != nil {
		_ = unstructured.SetNestedField(obj.Object, *minCount, "spec", "schedulingPolicy", "gang", "minCount")
	}
	obj.SetGroupVersionKind(podGroupGVK)
	return obj
}

func TestResolveGVK(t *testing.T) {
	cases := map[string]struct {
		mapper  apimeta.RESTMapper
		gk      schema.GroupKind
		wantGVK schema.GroupVersionKind
		wantOK  bool
	}{
		"resolved": {
			mapper:  restMapperFor(podGroupGVK),
			gk:      PodGroupGroupKind,
			wantGVK: podGroupGVK,
			wantOK:  true,
		},
		"not installed": {
			mapper: restMapperFor(),
			gk:     PodGroupGroupKind,
			wantOK: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gvk, ok, err := ResolveGVK(tc.mapper, tc.gk)
			if err != nil {
				t.Fatalf("ResolveGVK() error = %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ResolveGVK() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && gvk != tc.wantGVK {
				t.Fatalf("ResolveGVK() gvk = %v, want %v", gvk, tc.wantGVK)
			}
		})
	}
}

func TestPodGroupGangMinCount(t *testing.T) {
	four := int64(4)
	cases := map[string]struct {
		mapper       apimeta.RESTMapper
		objs         []client.Object
		wantMinCount int32
		wantFound    bool
		wantErr      bool
		namespace    string
		podGroupName string
	}{
		"found with gang policy": {
			mapper:       restMapperFor(podGroupGVK),
			objs:         []client.Object{podGroup("ns", "pg", &four)},
			namespace:    "ns",
			podGroupName: "pg",
			wantMinCount: 4,
			wantFound:    true,
		},
		"exists without gang policy": {
			mapper:       restMapperFor(podGroupGVK),
			objs:         []client.Object{podGroup("ns", "pg", nil)},
			namespace:    "ns",
			podGroupName: "pg",
			wantFound:    false,
		},
		"does not exist yet": {
			mapper:       restMapperFor(podGroupGVK),
			namespace:    "ns",
			podGroupName: "pg",
			wantFound:    false,
		},
		"API not installed": {
			mapper:       restMapperFor(),
			namespace:    "ns",
			podGroupName: "pg",
			wantFound:    false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newFakeClient(tc.mapper, tc.objs...)
			minCount, found, err := PodGroupGangMinCount(t.Context(), c, tc.namespace, tc.podGroupName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("PodGroupGangMinCount() error = %v, wantErr %v", err, tc.wantErr)
			}
			if found != tc.wantFound {
				t.Fatalf("PodGroupGangMinCount() found = %v, want %v", found, tc.wantFound)
			}
			if found && minCount != tc.wantMinCount {
				t.Fatalf("PodGroupGangMinCount() minCount = %v, want %v", minCount, tc.wantMinCount)
			}
		})
	}
}

func workload(namespace, name, ownerAPIGroup, ownerKind, ownerName string, templates ...map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
		"spec": map[string]any{
			"controllerRef": map[string]any{
				"apiGroup": ownerAPIGroup,
				"kind":     ownerKind,
				"name":     ownerName,
			},
		},
	}}
	if len(templates) > 0 {
		items := make([]any, 0, len(templates))
		for _, t := range templates {
			items = append(items, t)
		}
		_ = unstructured.SetNestedSlice(obj.Object, items, "spec", "podGroupTemplates")
	}
	obj.SetGroupVersionKind(workloadGVK)
	return obj
}

func podGroupTemplate(name string, minCount int64) map[string]any {
	tmpl := map[string]any{"name": name}
	_ = unstructured.SetNestedField(tmpl, minCount, "schedulingPolicy", "gang", "minCount")
	return tmpl
}

func TestPodGroupTemplateGangMinCounts(t *testing.T) {
	cases := map[string]struct {
		mapper    apimeta.RESTMapper
		objs      []client.Object
		namespace string
		apiGroup  string
		kind      string
		name      string
		want      map[string]int32
	}{
		"matching workload": {
			mapper: restMapperFor(workloadGVK),
			objs: []client.Object{
				workload("ns", "wl", "batch", "Job", "my-job",
					podGroupTemplate("driver", 1),
					podGroupTemplate("worker", 3),
				),
			},
			namespace: "ns",
			apiGroup:  "batch",
			kind:      "Job",
			name:      "my-job",
			want:      map[string]int32{"driver": 1, "worker": 3},
		},
		"no matching controllerRef": {
			mapper: restMapperFor(workloadGVK),
			objs: []client.Object{
				workload("ns", "wl", "batch", "Job", "other-job", podGroupTemplate("main", 2)),
			},
			namespace: "ns",
			apiGroup:  "batch",
			kind:      "Job",
			name:      "my-job",
			want:      nil,
		},
		"core API group owner": {
			mapper: restMapperFor(workloadGVK),
			objs: []client.Object{
				workload("ns", "wl", "", "Pod", "my-pod", podGroupTemplate("main", 2)),
			},
			namespace: "ns",
			apiGroup:  "",
			kind:      "Pod",
			name:      "my-pod",
			want:      map[string]int32{"main": 2},
		},
		"API not installed": {
			mapper:    restMapperFor(),
			namespace: "ns",
			apiGroup:  "batch",
			kind:      "Job",
			name:      "my-job",
			want:      nil,
		},
		"no workloads at all": {
			mapper:    restMapperFor(workloadGVK),
			namespace: "ns",
			apiGroup:  "batch",
			kind:      "Job",
			name:      "my-job",
			want:      nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newFakeClient(tc.mapper, tc.objs...)
			got, err := PodGroupTemplateGangMinCounts(t.Context(), c, tc.namespace, tc.apiGroup, tc.kind, tc.name)
			if err != nil {
				t.Fatalf("PodGroupTemplateGangMinCounts() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PodGroupTemplateGangMinCounts() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
