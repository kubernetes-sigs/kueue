/*
Copyright 2023 The Kubernetes Authors.

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

package admissioncheck

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestConfigHelper(t *testing.T) {
	testConfig := utiltesting.MakeProvisioningRequestConfig("config").
		ProvisioningClass("className").
		WithParameter("p1", "v1").
		WithManagedResource("cpu")

	cases := map[string]struct {
		admissioncheck       *kueue.AdmissionCheck
		config               *kueue.ProvisioningRequestConfig
		targetAdmissionCheck string
		wantConfig           *kueue.ProvisioningRequestConfig
		wantError            error
	}{
		"admission check bad name": {
			targetAdmissionCheck: "123",
			wantError:            cmpopts.AnyError,
		},
		"no parameter reference": {
			admissioncheck:       utiltesting.MakeAdmissionCheck("ac").Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrNilParametersRef,
		},
		"bad parameter reference, no name": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrBadParametersRef,
		},
		"bad parameter reference, bad group": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				Parameters("not-"+kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrBadParametersRef,
		},
		"bad parameter reference, bad kind": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "NptProvisioningRequestConfig", "config").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrBadParametersRef,
		},
		"config not found": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            cmpopts.AnyError,
		},
		"config found": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config").
				Obj(),
			config:               testConfig.DeepCopy(),
			targetAdmissionCheck: "ac",
			wantConfig:           testConfig.DeepCopy(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(kueue.AddToScheme(scheme))
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.admissioncheck != nil {
				builder = builder.WithObjects(tc.admissioncheck)
			}
			if tc.config != nil {
				builder = builder.WithObjects(tc.config)
			}
			client := builder.Build()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			helper, err := NewConfigHelper[*kueue.ProvisioningRequestConfig](client)

			if err != nil {
				t.Fatalf("cannot built the helper: %s", err)
			}

			gotConfig, gotError := helper.ConfigForAdmissionCheck(ctx, tc.targetAdmissionCheck)
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected config (-want/+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantConfig, gotConfig, cmpopts.IgnoreFields(kueue.ProvisioningRequestConfig{}, "TypeMeta", "ObjectMeta")); diff != "" {
				t.Errorf("unexpected config (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestIndexerFunc(t *testing.T) {
	cases := map[string]struct {
		admissioncheck *kueue.AdmissionCheck
		wantResult     []string
	}{
		"nil ac": {},
		"wrong controller": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				ControllerName("other-controller").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config-name").
				Obj(),
		},
		"wrong ref": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				ControllerName("test-controller").
				Parameters(kueue.GroupVersion.Group, "NotProvisioningRequestConfig", "config-name").
				Obj(),
		},
		"good": {
			admissioncheck: utiltesting.MakeAdmissionCheck("ac").
				ControllerName("test-controller").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config-name").
				Obj(),
			wantResult: []string{"config-name"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(kueue.AddToScheme(scheme))

			indexFnc := IndexerByConfigFunction("test-controller", kueue.GroupVersion.WithKind("ProvisioningRequestConfig"))

			gotResult := indexFnc(tc.admissioncheck)

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected result (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestFilterCheckStates(t *testing.T) {
	cases := map[string]struct {
		admissionchecks []kueue.AdmissionCheck
		states          []kueue.AdmissionCheckState
		wantResult      []string
	}{
		"empty": {},
		"no match": {

			states: []kueue.AdmissionCheckState{
				{Name: "check1"},
				{Name: "check2"},
				{Name: "check3"},
			},
		},
		"two matches": {
			admissionchecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("check1").ControllerName("test-controller").Obj(),
				*utiltesting.MakeAdmissionCheck("check2").ControllerName("other-controller").Obj(),
				*utiltesting.MakeAdmissionCheck("check3").ControllerName("test-controller").Obj(),
			},
			states: []kueue.AdmissionCheckState{
				{Name: "check1"},
				{Name: "check2"},
				{Name: "check3"},
			},
			wantResult: []string{"check1", "check3"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(kueue.AddToScheme(scheme))
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if len(tc.admissionchecks) > 0 {
				builder = builder.WithLists(&kueue.AdmissionCheckList{Items: tc.admissionchecks})
			}
			client := builder.Build()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotResult, _ := FilterForController(ctx, client, tc.states, "test-controller")

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected result (-want/+got):\n%s", diff)
			}
		})
	}
}
