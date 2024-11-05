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
	jsonpatch "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	testResourceKind = "TestResource"
	testResourceGVK  = schema.GroupVersionKind{Group: "foo.test.org", Version: "v1", Kind: testResourceKind}
)

type TestResource struct {
	Foo string `json:"foo,omitempty"`
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
		d.Foo = "bar"
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
				Raw: []byte(`{"baz": "qux"}`),
			},
		},
	}
	resp := handler.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Errorf("Response not allowed")
	}
	wantPatches := []jsonpatch.Operation{
		{
			Operation: "add",
			Path:      "/foo",
			Value:     "bar",
		},
	}
	if diff := cmp.Diff(wantPatches, resp.Patches); diff != "" {
		t.Errorf("Unexpected patches (-want, +got): %s", diff)
	}
}
