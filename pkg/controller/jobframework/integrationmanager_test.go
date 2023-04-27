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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testNewReconciler(*runtime.Scheme, client.Client, record.EventRecorder, ...Option) JobReconcilerInterface {
	return nil
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

var (
	testIntegrationCallbacks = IntegrationCallbacks{
		NewReconciler: testNewReconciler,
		SetupWebhook:  testSetupWebhook,
		JobType:       &corev1.Pod{},
		SetupIndexes:  testSetupIndexes,
		AddToScheme:   testAddToScheme,
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
				SetupWebhook: testSetupWebhook,
				JobType:      &corev1.Pod{},
				SetupIndexes: testSetupIndexes,
				AddToScheme:  testAddToScheme,
			},
			wantError: errMissingMadatoryField,
			wantList:  []string{},
		},
		"missing SetupWebhook": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler: testNewReconciler,
				JobType:       &corev1.Pod{},
				SetupIndexes:  testSetupIndexes,
				AddToScheme:   testAddToScheme,
			},
			wantError: errMissingMadatoryField,
			wantList:  []string{},
		},
		"missing JobType": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler: testNewReconciler,
				SetupWebhook:  testSetupWebhook,
				SetupIndexes:  testSetupIndexes,
				AddToScheme:   testAddToScheme,
			},
			wantError: errMissingMadatoryField,
			wantList:  []string{},
		},
		"missing SetupIndexes": {
			manager:         &integrationManager{},
			integrationName: "newFramework",
			integrationCallbacks: IntegrationCallbacks{
				NewReconciler: testNewReconciler,
				SetupWebhook:  testSetupWebhook,
				JobType:       &corev1.Pod{},
				AddToScheme:   testAddToScheme,
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
				NewReconciler: testNewReconciler,
				SetupWebhook:  testSetupWebhook,
				JobType:       &corev1.Pod{},
				SetupIndexes:  testSetupIndexes,
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

func TestForEach(t *testing.T) {
	foeEachError := errors.New("test error")
	cases := map[string]struct {
		registerd []string
		errorOn   string
		wantCalls []string
		wantError error
	}{
		"all": {
			registerd: []string{"a", "b", "c", "d", "e"},
			errorOn:   "",
			wantCalls: []string{"a", "b", "c", "d", "e"},
			wantError: nil,
		},
		"partial": {
			registerd: []string{"a", "b", "c", "d", "e"},
			errorOn:   "c",
			wantCalls: []string{"a", "b", "c"},
			wantError: foeEachError,
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			manager := integrationManager{}
			for _, name := range tc.registerd {
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
