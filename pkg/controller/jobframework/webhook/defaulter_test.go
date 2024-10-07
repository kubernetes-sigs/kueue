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
	Foo string   `json:"foo,omitempty"`
	Bar string   `json:"bar,omitempty"`
	Baz []string `json:"baz,omitempty"`

	Labels     map[string]string  `json:"labels,omitempty"`
	Finalizers []string           `json:"finalizers,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	SubResource TestSubResource `json:"subresource"`
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
	if len(d.Baz) > 0 {
		d.Baz = nil
	}

	if d.Labels != nil {
		delete(d.Labels, "foo")
	}
	if len(d.Finalizers) > 0 {
		finalizers := make([]string, 0, len(d.Finalizers))
		for _, val := range d.Finalizers {
			if val != "foo" {
				finalizers = append(finalizers, val)
			}
		}
		d.Finalizers = finalizers
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

	if d.SubResource.Foo == "foo" {
		d.SubResource.Foo = ""
	}
	if ptr.Deref(d.SubResource.Bar, "") == "bar" {
		d.SubResource.Bar = nil
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
	"unknown1": "unknown",
	"unknown2": ["unknown"],
	"bar": "bar", 
	"baz": ["foo"],
	"finalizers": ["foo","bar"],
	"labels": {"foo": "foo", "bar": "bar", "unknown": "unknown"},
	"subresource": {"unknown1": "unknown", "unknown2": ["unknown"], "foo": "foo", "bar": "bar"},
	"conditions": [
		{"type": "foo", "message": "foo", "reason": "", "status": "", "lastTransitionTime": null, "observedGeneration": 1, "unknown": "unknown"}, 
		{"type": "bar", "message": "bar", "reason": "", "status": "", "lastTransitionTime": null, "observedGeneration": 1, "unknown": "unknown"}
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
		{Operation: "remove", Path: "/baz"},
		{Operation: "replace", Path: "/finalizers/0", Value: "bar"},
		{Operation: "remove", Path: "/finalizers/1"},
		{Operation: "remove", Path: "/labels/foo"},
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

func TestConvertToFieldErrorPath(t *testing.T) {
	testCases := map[string]struct {
		path     string
		wantPath string
	}{
		"empty": {
			path:     "",
			wantPath: "",
		},
		"with slash": {
			path:     "/",
			wantPath: "",
		},
		"with one token": {
			path:     "/foo",
			wantPath: "foo",
		},
		"with two tokens": {
			path:     "/foo/bar",
			wantPath: "foo.bar",
		},
		"with element": {
			path:     "/foo/0",
			wantPath: "foo[0]",
		},
		"with field in array": {
			path:     "/foo/0/bar",
			wantPath: "foo[0].bar",
		},
		"with field in array deep": {
			path:     "/foo/1/bar/10/baz",
			wantPath: "foo[1].bar[10].baz",
		},
		"with field in array deep and element": {
			path:     "/foo/1/bar/10/baz/0",
			wantPath: "foo[1].bar[10].baz[0]",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotPath := convertToFieldErrorPath(tc.path)
			if diff := cmp.Diff(tc.wantPath, gotPath); diff != "" {
				t.Errorf("Unexpected path (-want, +got): %s", diff)
			}
		})
	}
}
