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
	metav1.ObjectMeta `json:",inline"`

	Foo string   `json:"foo,omitempty"`
	Bar string   `json:"bar,omitempty"`
	Baz []string `json:"baz,omitempty"`

	Conditions  []metav1.Condition `json:"conditions,omitempty"`
	Items       []TestResourceItem `json:"items,omitempty"`
	SubResource TestSubResource    `json:"subresource"`
}

type TestResourceItem struct {
	SubItems []TestResourceSubItem `json:"subItems,omitempty"`
}

type TestResourceSubItem struct {
	Baz []string `json:"baz,omitempty"`
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
		delete(d.Labels, "example.com/foo")
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

	if len(d.Items) > 0 && len(d.Items[0].SubItems) > 0 {
		baz := make([]string, 0, len(d.Items[0].SubItems[0].Baz))
		for _, val := range d.Items[0].SubItems[0].Baz {
			if val != "foo" {
				baz = append(baz, val)
			}
		}
		d.Items[0].SubItems[0].Baz = baz
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
	testCases := map[string]struct {
		request     admission.Request
		wantAllowed bool
		wantResult  *metav1.Status
		wantPatches []jsonpatch.Operation
	}{
		"valid request with unknown fields": {
			request: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Kind: metav1.GroupVersionKind(testResourceGVK),
					Object: runtime.RawExtension{
						// This raw object has fields not defined in the go type.
						// controller-runtime CustomDefaulter would have added remove operations for it.
						Raw: []byte(`{
	"unknown1": "unknown",
	"unknown2": ["unknown"],
	"unknown/unknown": "unknown",
	"bar": "bar", 
	"baz": ["foo"],
	"finalizers": ["foo","bar"],
	"labels": {"example.com/foo": "foo", "example.com/bar": "bar", "unknown": "unknown"},
	"subresource": {"unknown1": "unknown", "unknown2": ["unknown"], "foo": "foo", "bar": "bar"},
	"conditions": [
		{"type": "foo", "message": "foo", "reason": "", "status": "", "lastTransitionTime": null, "observedGeneration": 1, "unknown": "unknown"}, 
		{"type": "bar", "message": "bar", "reason": "", "status": "", "lastTransitionTime": null, "observedGeneration": 1, "unknown": "unknown"}
	],
	"items": [{"subItems": [{ "unknown1": "unknown", "baz": ["foo"] }] }]	
}`),
					},
				},
			},
			wantAllowed: true,
			wantPatches: []jsonpatch.Operation{
				{Operation: "add", Path: "/creationTimestamp"},
				{Operation: "add", Path: "/foo", Value: "foo"},
				{Operation: "remove", Path: "/bar"},
				{Operation: "remove", Path: "/baz"},
				{Operation: "replace", Path: "/finalizers/0", Value: "bar"},
				{Operation: "remove", Path: "/finalizers/1"},
				{Operation: "remove", Path: "/labels/example.com~1foo"},
				{Operation: "replace", Path: "/subresource/foo", Value: ""},
				{Operation: "replace", Path: "/subresource/bar"},
				{Operation: "remove", Path: "/conditions/0/observedGeneration"},
				{Operation: "remove", Path: "/conditions/1"},
				{Operation: "remove", Path: "/items/0/subItems/0/baz"},
			},
		},
		"invalid request": {
			request: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Kind: metav1.GroupVersionKind(testResourceGVK),
					Object: runtime.RawExtension{
						Raw: []byte(`{"foo": 1}`),
					},
				},
			},
			wantResult: &metav1.Status{
				Message: "json: cannot unmarshal number into Go struct field TestResource.foo of type string",
				Code:    400,
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			sch := runtime.NewScheme()
			builder := scheme.Builder{GroupVersion: testResourceGVK.GroupVersion()}
			builder.Register(&TestResource{})
			if err := builder.AddToScheme(sch); err != nil {
				t.Fatalf("Couldn't add types to scheme: %v", err)
			}

			handler := WithLosslessDefaulter(sch, &TestResource{}, &TestCustomDefaulter{})
			resp := handler.Handle(context.Background(), tc.request)

			if diff := cmp.Diff(tc.wantAllowed, resp.Allowed); diff != "" {
				t.Errorf("Unexpected allowed option (-want, +got): %s", diff)
			}

			if diff := cmp.Diff(tc.wantResult, resp.Result); diff != "" {
				t.Errorf("Unexpected result (-want, +got): %s", diff)
			}

			if diff := cmp.Diff(tc.wantPatches, resp.Patches, cmpopts.SortSlices(func(a, b jsonpatch.Operation) bool {
				return a.Path < b.Path
			})); diff != "" {
				t.Errorf("Unexpected patches (-want, +got): %s", diff)
			}
		})
	}
}
