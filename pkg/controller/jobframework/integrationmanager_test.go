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

package jobframework

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type testReconciler struct{}

func (t *testReconciler) Reconcile(context.Context, reconcile.Request) (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

func (t *testReconciler) SetupWithManager(mgr ctrlmgr.Manager) error {
	return nil
}

var _ JobReconcilerInterface = (*testReconciler)(nil)

func testNewReconciler(client.Client, record.EventRecorder, ...Option) JobReconcilerInterface {
	return &testReconciler{}
}

func testSetupWebhook(ctrl.Manager, ...Option) error {
	return nil
}

func testSetupIndexes(context.Context, client.FieldIndexer) error {
	return nil
}

func testAddToScheme(*runtime.Scheme) error {
	return nil
}

func testCanSupportIntegration(...Option) (bool, error) {
	return true, nil
}

var (
	testIntegrationCallbacks = IntegrationCallbacks{
		NewReconciler:         testNewReconciler,
		SetupWebhook:          testSetupWebhook,
		JobType:               &corev1.Pod{},
		SetupIndexes:          testSetupIndexes,
		AddToScheme:           testAddToScheme,
		CanSupportIntegration: testCanSupportIntegration,
	}
)

func TestRegister(t *testing.T) {
	cases := map[string]struct {
		manager              *integrationManager
		integrationName      string
		integrationCallbacks IntegrationCallbacks
		wantError            error
		wantList             []string
		wantCallbacks        IntegrationCallbacks
	}{
		"successful": {
			manager: &integrationManager{
				names: []string{"oldFramework"},
				integrations: map[string]IntegrationCallbacks{
					"oldFramework": testIntegrationCallbacks,
				},
			},
			integrationName:      "newFramework",
			integrationCallbacks: testIntegrationCallbacks,
			wantError:            nil,
			wantList:             []string{"newFramework", "oldFramework"},
			wantCallbacks:        testIntegrationCallbacks,
		},
		"duplicate name": {
			manager: &integrationManager{
				names: []string{"newFramework"},
				integrations: map[string]IntegrationCallbacks{
					"newFramework": testIntegrationCallbacks,
				},
			},
			integrationName:      "newFramework",
			integrationCallbacks: IntegrationCallbacks{},
			wantError:            errDuplicateFrameworkName,
			wantList:             []string{"newFramework"},
			wantCallbacks:        testIntegrationCallbacks,
		},
		"missing NewReconciler": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				SetupWebhook:          testSetupWebhook,
				JobType:               &corev1.Pod{},
				SetupIndexes:          testSetupIndexes,
				AddToScheme:           testAddToScheme,
				CanSupportIntegration: testCanSupportIntegration,
			},
			wantError: errMissingMandatoryField,
			wantList:  []string{},
		},
		"missing SetupWebhook": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler:         testNewReconciler,
				JobType:               &corev1.Pod{},
				SetupIndexes:          testSetupIndexes,
				AddToScheme:           testAddToScheme,
				CanSupportIntegration: testCanSupportIntegration,
			},
			wantError: errMissingMandatoryField,
			wantList:  []string{},
		},
		"missing JobType": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler:         testNewReconciler,
				SetupWebhook:          testSetupWebhook,
				SetupIndexes:          testSetupIndexes,
				AddToScheme:           testAddToScheme,
				CanSupportIntegration: testCanSupportIntegration,
			},
			wantError: errMissingMandatoryField,
			wantList:  []string{},
		},
		"missing SetupIndexes": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler:         testNewReconciler,
				SetupWebhook:          testSetupWebhook,
				JobType:               &corev1.Pod{},
				AddToScheme:           testAddToScheme,
				CanSupportIntegration: testCanSupportIntegration,
			},
			wantError: nil,
			wantList:  []string{"newFramework"},
			wantCallbacks: IntegrationCallbacks{
				NewReconciler: testNewReconciler,
				SetupWebhook:  testSetupWebhook,
				JobType:       &corev1.Pod{},
				AddToScheme:   testAddToScheme,
			},
		},
		"missing AddToScheme": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler:         testNewReconciler,
				SetupWebhook:          testSetupWebhook,
				JobType:               &corev1.Pod{},
				SetupIndexes:          testSetupIndexes,
				CanSupportIntegration: testCanSupportIntegration,
			},
			wantError: nil,
			wantList:  []string{"newFramework"},
			wantCallbacks: IntegrationCallbacks{
				NewReconciler: testNewReconciler,
				SetupWebhook:  testSetupWebhook,
				JobType:       &corev1.Pod{},
				SetupIndexes:  testSetupIndexes,
			},
		},
		"missing CanSupportIntegration": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler: testNewReconciler,
				AddToScheme:   testAddToScheme,
				SetupWebhook:  testSetupWebhook,
				JobType:       &corev1.Pod{},
				SetupIndexes:  testSetupIndexes,
			},
			wantError: nil,
			wantList:  []string{"newFramework"},
			wantCallbacks: IntegrationCallbacks{
				NewReconciler: testNewReconciler,
				AddToScheme:   testAddToScheme,
				SetupWebhook:  testSetupWebhook,
				JobType:       &corev1.Pod{},
				SetupIndexes:  testSetupIndexes,
			},
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			gotError := tc.manager.register(tc.integrationName, tc.integrationCallbacks)
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want +got):\n%s", diff)
			}
			gotList := tc.manager.getList()
			if diff := cmp.Diff(tc.wantList, gotList); diff != "" {
				t.Errorf("Unexpected frameworks list (-want +got):\n%s", diff)
			}

			if gotCallbacks, found := tc.manager.get(tc.integrationName); found {
				if diff := cmp.Diff(tc.wantCallbacks, gotCallbacks, cmp.FilterValues(func(_, _ interface{}) bool { return true }, cmp.Comparer(compareCallbacks))); diff != "" {
					t.Errorf("Unexpected callbacks (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func compareCallbacks(x, y interface{}) bool {
	xcb := x.(IntegrationCallbacks)
	ycb := y.(IntegrationCallbacks)

	if reflect.ValueOf(xcb.NewReconciler).Pointer() != reflect.ValueOf(ycb.NewReconciler).Pointer() {
		return false
	}
	if reflect.ValueOf(xcb.SetupWebhook).Pointer() != reflect.ValueOf(ycb.SetupWebhook).Pointer() {
		return false
	}
	if reflect.TypeOf(xcb.JobType) != reflect.TypeOf(ycb.JobType) {
		return false
	}
	if reflect.ValueOf(xcb.SetupIndexes).Pointer() != reflect.ValueOf(ycb.SetupIndexes).Pointer() {
		return false
	}
	return reflect.ValueOf(xcb.AddToScheme).Pointer() == reflect.ValueOf(ycb.AddToScheme).Pointer()
}

func TestRegisterExternal(t *testing.T) {
	cases := map[string]struct {
		manager   *integrationManager
		kindArg   string
		wantError error
		wantGVK   *schema.GroupVersionKind
	}{
		"successful 1": {
			manager: &integrationManager{
				names: []string{"oldFramework"},
				integrations: map[string]IntegrationCallbacks{
					"oldFramework": testIntegrationCallbacks,
				},
			},
			kindArg:   "Job.v1.batch",
			wantError: nil,
			wantGVK:   &schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
		},
		"successful 2": {
			manager: &integrationManager{
				externalIntegrations: map[string]runtime.Object{
					"Job.v1.batch": &batchv1.Job{TypeMeta: metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"}},
				},
			},
			kindArg:   "AppWrapper.v1beta2.workload.codeflare.dev",
			wantError: nil,
			wantGVK:   &schema.GroupVersionKind{Group: "workload.codeflare.dev", Version: "v1beta2", Kind: "AppWrapper"},
		},
		"malformed kind arg": {
			manager:   &integrationManager{},
			kindArg:   "batch/job",
			wantError: errFrameworkNameFormat,
			wantGVK:   nil,
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			gotError := tc.manager.registerExternal(tc.kindArg)
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want +got):\n%s", diff)
			}
			if gotJobType, found := tc.manager.getExternal(tc.kindArg); found {
				gvk := gotJobType.GetObjectKind().GroupVersionKind()
				if diff := cmp.Diff(tc.wantGVK, &gvk); diff != "" {
					t.Errorf("Unexpected jobtypes (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestForEach(t *testing.T) {
	foeEachError := errors.New("test error")
	cases := map[string]struct {
		registered []string
		errorOn    string
		wantCalls  []string
		wantError  error
	}{
		"all": {
			registered: []string{"a", "b", "c", "d", "e"},
			errorOn:    "",
			wantCalls:  []string{"a", "b", "c", "d", "e"},
			wantError:  nil,
		},
		"partial": {
			registered: []string{"a", "b", "c", "d", "e"},
			errorOn:    "c",
			wantCalls:  []string{"a", "b", "c"},
			wantError:  foeEachError,
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			manager := integrationManager{}
			for _, name := range tc.registered {
				if err := manager.register(name, testIntegrationCallbacks); err != nil {
					t.Fatalf("unable to register %s, %s", name, err.Error())
				}
			}

			gotCalls := []string{}
			gotError := manager.forEach(func(name string, cb IntegrationCallbacks) error {
				gotCalls = append(gotCalls, name)
				if name == tc.errorOn {
					return foeEachError
				}
				return nil
			})

			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCalls, gotCalls); diff != "" {
				t.Errorf("Unexpected calls list (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetJobTypeForOwner(t *testing.T) {
	dontManage := IntegrationCallbacks{
		NewReconciler: func(client.Client, record.EventRecorder, ...Option) JobReconcilerInterface {
			panic("not implemented")
		},
		SetupWebhook: func(ctrl.Manager, ...Option) error { panic("not implemented") },
		JobType:      nil,
	}
	manageK1 := func() IntegrationCallbacks {
		ret := dontManage
		ret.IsManagingObjectsOwner = func(owner *metav1.OwnerReference) bool { return owner.Kind == "K1" }
		ret.JobType = &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "K1"}}
		return ret
	}()
	manageK2 := func() IntegrationCallbacks {
		ret := dontManage
		ret.IsManagingObjectsOwner = func(owner *metav1.OwnerReference) bool { return owner.Kind == "K2" }
		ret.JobType = &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "K2"}}
		return ret
	}()
	externalK3 := func() runtime.Object {
		return &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "K3"}}
	}()
	disabledK4 := func() IntegrationCallbacks {
		ret := dontManage
		ret.IsManagingObjectsOwner = func(owner *metav1.OwnerReference) bool { return owner.Kind == "K4" }
		ret.JobType = &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "K4"}}
		return ret
	}()

	mgr := integrationManager{
		names: []string{"manageK1", "dontManage", "manageK2", "disabledK4"},
		integrations: map[string]IntegrationCallbacks{
			"dontManage": dontManage,
			"manageK1":   manageK1,
			"manageK2":   manageK2,
			"disabledK4": disabledK4,
		},
		externalIntegrations: map[string]runtime.Object{
			"externalK3": externalK3,
		},
	}
	mgr.enableIntegration("dontManage")
	mgr.enableIntegration("manageK1")
	mgr.enableIntegration("manageK2")

	cases := map[string]struct {
		owner       *metav1.OwnerReference
		wantJobType runtime.Object
	}{
		"K1": {
			owner:       &metav1.OwnerReference{Kind: "K1"},
			wantJobType: manageK1.JobType,
		},
		"K2": {
			owner:       &metav1.OwnerReference{Kind: "K2"},
			wantJobType: manageK2.JobType,
		},
		"K3": {
			owner:       &metav1.OwnerReference{Kind: "K3"},
			wantJobType: externalK3,
		},
		"K4": {
			owner:       &metav1.OwnerReference{Kind: "K4"},
			wantJobType: nil,
		},
		"K5": {
			owner:       &metav1.OwnerReference{Kind: "K5"},
			wantJobType: nil,
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			wantJobType := mgr.getJobTypeForOwner(tc.owner)
			if tc.wantJobType == nil {
				if wantJobType != nil {
					t.Errorf("This owner should be unmanaged")
				}
			} else {
				if wantJobType == nil {
					t.Errorf("This owner should be managed")
				} else {
					if diff := cmp.Diff(tc.wantJobType, wantJobType); diff != "" {
						t.Errorf("Unexpected callbacks (-want +got):\n%s", diff)
					}
				}
			}
		})
	}
}

func TestEnabledIntegrationsDependencies(t *testing.T) {
	cases := map[string]struct {
		integrationsDependencies map[string][]string
		enabled                  []string
		wantError                error
	}{
		"empty": {},
		"not found": {
			enabled:   []string{"i1"},
			wantError: errIntegrationNotFound,
		},
		"dependecncy not enabled": {
			integrationsDependencies: map[string][]string{
				"i1": {"i2"},
			},
			enabled:   []string{"i1"},
			wantError: errDependencyIntegrationNotEnabled,
		},
		"dependecncy not found": {
			integrationsDependencies: map[string][]string{
				"i1": {"i2"},
			},
			enabled:   []string{"i1", "i2"},
			wantError: errIntegrationNotFound,
		},
		"no error": {
			integrationsDependencies: map[string][]string{
				"i1": {"i2", "i3"},
				"i2": {"i3"},
				"i3": nil,
			},
			enabled: []string{"i1", "i2", "i3"},
		},
	}
	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			manager := integrationManager{
				integrations: map[string]IntegrationCallbacks{},
			}
			for inegration, deps := range tc.integrationsDependencies {
				manager.integrations[inegration] = IntegrationCallbacks{
					DependencyList: deps,
				}
			}
			gotError := manager.checkEnabledListDependencies(sets.New(tc.enabled...))
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected check error (-want +got):\n%s", diff)
			}
		})
	}
}
