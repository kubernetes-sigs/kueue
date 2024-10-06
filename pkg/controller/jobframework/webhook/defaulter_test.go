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

package webhook

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	testResourceKind = "TestResource"
	testResourceGVK  = schema.GroupVersionKind{Group: "foo.test.org", Version: "v1", Kind: testResourceKind}
)

type TestResource struct {
	Foo  string   `json:"foo,omitempty"`
	Bar  string   `json:"bar,omitempty"`
	Qux  []string `json:"qux,omitempty"`
	Quux []string `json:"quux,omitempty"`

	SubResource TestSubResource    `json:"subresource"`
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
}

type TestSubResource struct {
	Foo string  `json:"foo"`
	Bar *string `json:"bar"`
}

func (d *TestResource) GetObjectKind() schema.ObjectKind { return d }
func (d *TestResource) DeepCopyObject() runtime.Object {
	return &TestResource{
		Foo: d.Foo,
	}
}

func (d *TestResource) GroupVersionKind() schema.GroupVersionKind {
	return testResourceGVK
}

func (d *TestResource) SetGroupVersionKind(gvk schema.GroupVersionKind) {}

type TestCustomDefaulter struct{}

func (*TestCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	d := obj.(*TestResource)
	if d.Foo == "" {
		d.Foo = "foo"
	}
	if d.Bar == "bar" {
		d.Bar = ""
	}
	if d.SubResource.Foo == "foo" {
		d.SubResource.Foo = ""
	}
	if ptr.Deref(d.SubResource.Bar, "") == "bar" {
		d.SubResource.Bar = nil
	}
	if len(d.Qux) > 0 {
		d.Qux = nil
	}
	if len(d.Quux) > 0 {
		quux := make([]string, 0, len(d.Quux))
		for _, val := range d.Quux {
			if val != "foo" {
				quux = append(quux, val)
			}
		}
		d.Quux = quux
	}
	if len(d.Conditions) > 0 {
		conditions := make([]metav1.Condition, 0, len(d.Conditions))
		for _, cond := range d.Conditions {
			if cond.Type == "foo" {
				cond.ObservedGeneration = 0
				conditions = append(conditions, cond)
			}
		}
		d.Conditions = conditions
	}
	return nil
}

func TestLossLessDefaulter(t *testing.T) {
	sch := runtime.NewScheme()
	builder := scheme.Builder{GroupVersion: testResourceGVK.GroupVersion()}
	builder.Register(&TestResource{})
	if err := builder.AddToScheme(sch); err != nil {
		t.Fatalf("Couldn't add types to scheme: %v", err)
	}

	handler := WithLosslessDefaulter(sch, &TestResource{}, &TestCustomDefaulter{})

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Kind: metav1.GroupVersionKind(testResourceGVK),
			Object: runtime.RawExtension{
				// This raw object has a field not defined in the go type.
				// controller-runtime CustomDefaulter would have added a remove operation for it.
				Raw: []byte(`{
	"hoge": "hoge",
	"fuga": ["hoge"],
	"bar": "bar", 
	"qux": ["foo"],
	"quux": ["foo","bar"],
	"subresource": {"hoge": "hoge", "fuga": ["hoge"], "foo": "foo", "bar": "bar"},
	"conditions": [
		{"type": "foo", "message": "foo", "reason": "", "status": "", "lastTransitionTime": null, "observedGeneration": 1}, 
		{"type": "bar", "message": "bar", "reason": "", "status": "", "lastTransitionTime": null, "observedGeneration": 1}
	]
}`),
			},
		},
	}
	resp := handler.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Errorf("Response not allowed")
	}
	wantPatches := []jsonpatch.Operation{
		{Operation: "add", Path: "/foo", Value: "foo"},
		{Operation: "remove", Path: "/bar"},
		{Operation: "remove", Path: "/qux"},
		{Operation: "replace", Path: "/quux/0", Value: "bar"},
		{Operation: "remove", Path: "/quux/1"},
		{Operation: "replace", Path: "/subresource/foo", Value: ""},
		{Operation: "replace", Path: "/subresource/bar"},
		{Operation: "remove", Path: "/conditions/0/observedGeneration"},
		{Operation: "remove", Path: "/conditions/1"},
	}
	if diff := cmp.Diff(wantPatches, resp.Patches, cmpopts.SortSlices(func(a, b jsonpatch.Operation) bool {
		return a.Path < b.Path
	})); diff != "" {
		t.Errorf("Unexpected patches (-want, +got): %s", diff)
	}
}

func TestFieldExistsByJSONPath(t *testing.T) {
	testCases := map[string]struct {
		obj        interface{}
		path       string
		wantExists bool
	}{
		"nil obj type": {
			path:       "/foo",
			wantExists: false,
		},
		"empty path": {
			obj: struct {
				Foo struct{} `json:"foo,omitempty"`
			}{},
			path:       "",
			wantExists: false,
		},
		"invalid path 1": {
			obj: struct {
				Foo struct{} `json:"foo,omitempty"`
			}{},
			path:       "/",
			wantExists: false,
		},
		"invalid path 2": {
			obj: struct {
				Foo struct{} `json:"foo,omitempty"`
			}{},
			path:       "foo/",
			wantExists: false,
		},
		"invalid path 3": {
			obj: struct {
				Foo struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "foo/bar",
			wantExists: false,
		},
		"invalid path 4": {
			obj: struct {
				Foo struct{} `json:"foo,omitempty"`
			}{},
			path:       "/foo/",
			wantExists: false,
		},
		"invalid object type": {
			obj:        "invalid",
			path:       "/foo",
			wantExists: false,
		},
		"exists": {
			obj: struct {
				Foo string
				Bar string `json:"bar"`
			}{},
			path:       "/bar",
			wantExists: true,
		},
		"exists with slice": {
			obj: struct {
				Foo []struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo",
			wantExists: true,
		},
		"exists with slice element": {
			obj: struct {
				Foo []struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/0",
			wantExists: true,
		},
		"exists with slice element field": {
			obj: struct {
				Foo []struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/0/bar",
			wantExists: true,
		},
		"exists with array": {
			obj: struct {
				Foo [0]struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo",
			wantExists: true,
		},
		"exists with array element": {
			obj: struct {
				Foo [0]struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/0",
			wantExists: true,
		},
		"exists with array element field": {
			obj: struct {
				Foo [0]struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/0/bar",
			wantExists: true,
		},
		"exists without tag": {
			obj: struct {
				Foo string
			}{},
			path:       "/Foo",
			wantExists: true,
		},
		"exists number": {
			obj: struct {
				Foo string `json:"123,omitempty"`
			}{},
			path:       "/123",
			wantExists: true,
		},
		"exists nested": {
			obj: struct {
				Foo struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/bar",
			wantExists: true,
		},
		"exists nested with pointer": {
			obj: struct {
				Foo *struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{
				&struct {
					Bar string `json:"bar,omitempty"`
				}{
					Bar: "bar",
				},
			},
			path:       "/foo/bar",
			wantExists: true,
		},
		"exists nested with nil pointer": {
			obj: struct {
				Foo *struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/bar",
			wantExists: true,
		},
		"not exists": {
			obj:        struct{}{},
			path:       "/foo",
			wantExists: false,
		},
		"invalid type": {
			obj: struct {
				Foo func() `json:"foo,omitempty"`
			}{},
			path:       "/foo/bar",
			wantExists: false,
		},
		"not exists nested": {
			obj:        struct{}{},
			path:       "/foo/bar/quz",
			wantExists: false,
		},
		"not exists nested with object": {
			obj: struct {
				Foo struct{} `json:"foo,omitempty"`
			}{},
			path:       "/foo/bar/quz",
			wantExists: false,
		},
		"not exists with slice element field": {
			obj: struct {
				Foo []struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/1/baz",
			wantExists: false,
		},
		"not exists with array element field": {
			obj: struct {
				Foo [0]struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/1/baz",
			wantExists: false,
		},
		"not exists nested with pointer": {
			obj: struct {
				Foo *struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{
				&struct {
					Bar string `json:"bar,omitempty"`
				}{
					Bar: "bar",
				},
			},
			path:       "/foo/baz",
			wantExists: false,
		},
		"not exists nested with nil pointer": {
			obj: struct {
				Foo *struct {
					Bar string `json:"bar,omitempty"`
				} `json:"foo,omitempty"`
			}{},
			path:       "/foo/baz",
			wantExists: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotExists := fieldExistsByJSONPath(tc.obj, tc.path)
			if diff := cmp.Diff(tc.wantExists, gotExists); diff != "" {
				t.Errorf("Unexpected exists (-want, +got): %s", diff)
			}
		})
	}
}

func TestGetFieldName(t *testing.T) {
	testCases := map[string]struct {
		field    reflect.StructField
		wantName string
	}{
		"empty": {},
		"with json tag": {
			field:    reflect.StructField{Name: "Foo", Tag: `json:"foo"`},
			wantName: "foo",
		},
		"without json tag": {
			field:    reflect.StructField{Name: "Foo", Tag: `foo:"bar"`},
			wantName: "Foo",
		},
		"with json tag and omitempty": {
			field:    reflect.StructField{Name: "Foo", Tag: `json:"foo,omitempty"`},
			wantName: "foo",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotName := getFieldName(tc.field)
			if diff := cmp.Diff(tc.wantName, gotName); diff != "" {
				t.Errorf("Unexpected field name (-want, +got): %s", diff)
			}
		})
	}
}

func TestIsInt(t *testing.T) {
	testCases := map[string]struct {
		str        string
		wantResult bool
	}{
		"empty": {
			str:        "123",
			wantResult: true,
		},
		"int": {
			str:        "123",
			wantResult: true,
		},
		"not int": {
			str:        "string",
			wantResult: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotResult := isInt(tc.str)
			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("Unexpected result (-want, +got): %s", diff)
			}
		})
	}
}
