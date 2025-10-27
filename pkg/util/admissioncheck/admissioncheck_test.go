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

package admissioncheck

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestConfigHelper(t *testing.T) {
	testConfig := utiltestingapi.MakeProvisioningRequestConfig("config").
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
			admissioncheck:       utiltestingapi.MakeAdmissionCheck("ac").Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrNilParametersRef,
		},
		"bad parameter reference, no name": {
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrBadParametersRef,
		},
		"bad parameter reference, bad group": {
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
				Parameters("not-"+kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrBadParametersRef,
		},
		"bad parameter reference, bad kind": {
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "NptProvisioningRequestConfig", "config").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            ErrBadParametersRef,
		},
		"config not found": {
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config").
				Obj(),
			targetAdmissionCheck: "ac",
			wantError:            cmpopts.AnyError,
		},
		"config found": {
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config").
				Obj(),
			config:               testConfig.DeepCopy(),
			targetAdmissionCheck: "ac",
			wantConfig:           testConfig.DeepCopy(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			if tc.admissioncheck != nil {
				builder = builder.WithObjects(tc.admissioncheck)
			}
			if tc.config != nil {
				builder = builder.WithObjects(tc.config)
			}
			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			helper, err := NewConfigHelper[*kueue.ProvisioningRequestConfig](client)

			if err != nil {
				t.Fatalf("cannot built the helper: %s", err)
			}

			gotConfig, gotError := helper.ConfigForAdmissionCheck(ctx, kueue.AdmissionCheckReference(tc.targetAdmissionCheck))
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
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
				ControllerName("other-controller").
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "config-name").
				Obj(),
		},
		"wrong ref": {
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
				ControllerName("test-controller").
				Parameters(kueue.GroupVersion.Group, "NotProvisioningRequestConfig", "config-name").
				Obj(),
		},
		"good": {
			admissioncheck: utiltestingapi.MakeAdmissionCheck("ac").
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
		wantResult      []kueue.AdmissionCheckReference
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
				*utiltestingapi.MakeAdmissionCheck("check1").ControllerName("test-controller").Obj(),
				*utiltestingapi.MakeAdmissionCheck("check2").ControllerName("other-controller").Obj(),
				*utiltestingapi.MakeAdmissionCheck("check3").ControllerName("test-controller").Obj(),
			},
			states: []kueue.AdmissionCheckState{
				{Name: "check1"},
				{Name: "check2"},
				{Name: "check3"},
			},
			wantResult: []kueue.AdmissionCheckReference{"check1", "check3"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			if len(tc.admissionchecks) > 0 {
				builder = builder.WithLists(&kueue.AdmissionCheckList{Items: tc.admissionchecks})
			}
			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)
			gotResult, _ := FilterForController(ctx, client, tc.states, "test-controller")

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected result (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestGetMultiKueueAdmissionCheck(t *testing.T) {
	cases := map[string]struct {
		admissionChecks []kueue.AdmissionCheck
		workloadACS     []kueue.AdmissionCheckState
		wantResult      *kueue.AdmissionCheckState
		wantErr         error
	}{
		"empty workload states": {
			workloadACS: []kueue.AdmissionCheckState{},
			wantResult:  nil,
			wantErr:     nil,
		},
		"no relevant checks": {
			admissionChecks: []kueue.AdmissionCheck{
				*utiltestingapi.MakeAdmissionCheck("check1").ControllerName("other-controller").Obj(),
				*utiltestingapi.MakeAdmissionCheck("check2").ControllerName("other-controller").Obj(),
			},
			workloadACS: []kueue.AdmissionCheckState{
				{Name: "check1"},
				{Name: "check2"},
			},
			wantResult: nil,
			wantErr:    nil,
		},
		"one relevant check": {
			admissionChecks: []kueue.AdmissionCheck{
				*utiltestingapi.MakeAdmissionCheck("check1").ControllerName(kueue.MultiKueueControllerName).Obj(),
				*utiltestingapi.MakeAdmissionCheck("check2").ControllerName("other-controller").Obj(),
			},
			workloadACS: []kueue.AdmissionCheckState{
				{Name: "check1"},
				{Name: "check2"},
			},
			wantResult: &kueue.AdmissionCheckState{Name: "check1"},
			wantErr:    nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			if len(tc.admissionChecks) > 0 {
				builder = builder.WithLists(&kueue.AdmissionCheckList{Items: tc.admissionChecks})
			}
			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			workload := &kueue.Workload{
				Status: kueue.WorkloadStatus{
					AdmissionChecks: tc.workloadACS,
				},
			}

			gotResult, err := GetMultiKueueAdmissionCheck(ctx, client, workload)

			if diff := cmp.Diff(tc.wantErr, err); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected result (-want/+got):\n%s", diff)
			}
		})
	}
}
func TestGetRemoteClusters(t *testing.T) {
	multiKueueConfigGetError := errors.New("failed to get MultiKueueConfig")
	acTest := "test-admission-check"
	cases := map[string]struct {
		multiKueueConfig *kueue.MultiKueueConfig
		wantResult       sets.Set[string]
		wantErr          error
	}{
		"no clusters": {
			multiKueueConfig: &kueue.MultiKueueConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: acTest,
				},
				Spec: kueue.MultiKueueConfigSpec{
					Clusters: []string{},
				},
			},
			wantResult: nil,
			wantErr:    ErrNoActiveClusters,
		},
		"multiple clusters": {
			multiKueueConfig: &kueue.MultiKueueConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: acTest,
				},
				Spec: kueue.MultiKueueConfigSpec{
					Clusters: []string{"cluster1", "cluster2"},
				},
			},
			wantResult: sets.New("cluster1", "cluster2"),
		},
		"error fetching config": {
			multiKueueConfig: nil,
			wantResult:       nil,
			wantErr:          multiKueueConfigGetError,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, isMKCfg := obj.(*kueue.MultiKueueConfig); isMKCfg && errors.Is(tc.wantErr, multiKueueConfigGetError) {
						return tc.wantErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			ac := utiltestingapi.MakeAdmissionCheck(acTest).
				ControllerName(kueue.MultiKueueControllerName).
				Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", acTest).
				Obj()
			builder = builder.WithObjects(ac)

			if tc.multiKueueConfig != nil {
				builder = builder.WithObjects(tc.multiKueueConfig)
			}
			client := builder.Build()

			helper, err := NewMultiKueueStoreHelper(client)
			if err != nil {
				t.Fatalf("failed to create MultiKueueStoreHelper: %v", err)
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			gotResult, err := GetRemoteClusters(ctx, helper, kueue.AdmissionCheckReference(acTest))

			if err != nil {
				if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("unexpected error (-want/+got):\n%s", diff)
				}
			}

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected result (-want/+got):\n%s", diff)
			}
		})
	}
}
