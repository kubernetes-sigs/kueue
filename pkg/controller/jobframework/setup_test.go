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

package jobframework

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestSetupControllers(t *testing.T) {
	availableIntegrations := map[string]IntegrationCallbacks{
		"batch/job": {
			NewReconciler:         testNewReconciler,
			SetupWebhook:          testSetupWebhook,
			JobType:               &batchv1.Job{},
			SetupIndexes:          testSetupIndexes,
			AddToScheme:           testAddToScheme,
			CanSupportIntegration: testCanSupportIntegration,
		},
		"kubeflow.org/mpijob": {
			NewReconciler:         testNewReconciler,
			SetupWebhook:          testSetupWebhook,
			JobType:               &kfmpi.MPIJob{},
			SetupIndexes:          testSetupIndexes,
			AddToScheme:           testAddToScheme,
			CanSupportIntegration: testCanSupportIntegration,
		},
		"pod": {
			NewReconciler:         testNewReconciler,
			SetupWebhook:          testSetupWebhook,
			JobType:               &corev1.Pod{},
			SetupIndexes:          testSetupIndexes,
			AddToScheme:           testAddToScheme,
			CanSupportIntegration: testCanSupportIntegration,
		},
		"ray.io/raycluster": {
			NewReconciler:         testNewReconciler,
			SetupWebhook:          testSetupWebhook,
			JobType:               &rayv1.RayCluster{},
			SetupIndexes:          testSetupIndexes,
			AddToScheme:           testAddToScheme,
			CanSupportIntegration: testCanSupportIntegration,
		},
	}

	cases := map[string]struct {
		opts                    []Option
		mapperGVKs              []schema.GroupVersionKind
		delayedGVKs             []*schema.GroupVersionKind
		wantError               error
		wantEnabledIntegrations []string
	}{
		"setup controllers succeed": {
			opts: []Option{
				WithEnabledFrameworks([]string{"batch/job", "kubeflow.org/mpijob"}),
				WithEnabledExternalFrameworks([]string{
					"Foo.v1.example.com",
					"Bar.v2.example.com",
				}),
			},
			mapperGVKs: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
				kfmpi.SchemeGroupVersionKind,
			},
			wantEnabledIntegrations: []string{"batch/job", "kubeflow.org/mpijob"},
		},
		"mapper doesn't have kubeflow.org/mpijob, but no error occur": {
			opts: []Option{
				WithEnabledFrameworks([]string{"batch/job", "kubeflow.org/mpijob"}),
			},
			mapperGVKs: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
			},
			wantEnabledIntegrations: []string{"batch/job"},
		},
		"mapper doesn't have ray.io/raycluster when Controllers have been setup, but eventually does": {
			opts: []Option{
				WithEnabledFrameworks([]string{"batch/job", "kubeflow.org/mpijob", "ray.io/raycluster"}),
			},
			mapperGVKs: []schema.GroupVersionKind{
				batchv1.SchemeGroupVersion.WithKind("Job"),
				kfmpi.SchemeGroupVersionKind,
				// Not including RayCluster
			},
			delayedGVKs: []*schema.GroupVersionKind{
				{Group: "ray.io", Version: "v1", Kind: "RayCluster"},
			},
			wantEnabledIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "ray.io/raycluster"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manager := integrationManager{}
			for name, cbs := range availableIntegrations {
				err := manager.register(name, cbs)
				if err != nil {
					t.Fatalf("Unexpected error while registering %q: %s", name, err)
				}
			}

			ctx, logger := utiltesting.ContextWithLog(t)
			k8sClient := utiltesting.NewClientBuilder(jobset.AddToScheme, kfmpi.AddToScheme, kftraining.AddToScheme, rayv1.AddToScheme).Build()

			mgrOpts := ctrlmgr.Options{
				Scheme: k8sClient.Scheme(),
				NewClient: func(*rest.Config, client.Options) (client.Client, error) {
					return k8sClient, nil
				},
				MapperProvider: func(*rest.Config, *http.Client) (apimeta.RESTMapper, error) {
					gvs := slices.Map(tc.mapperGVKs, func(gvk *schema.GroupVersionKind) schema.GroupVersion {
						return gvk.GroupVersion()
					})
					mapper := apimeta.NewDefaultRESTMapper(gvs)
					testMapper := &TestRESTMapper{
						DefaultRESTMapper: mapper,
						lock:              sync.RWMutex{},
					}
					for _, gvk := range tc.mapperGVKs {
						testMapper.Add(gvk, apimeta.RESTScopeNamespace)
					}
					return testMapper, nil
				},
			}
			mgr, err := ctrlmgr.New(&rest.Config{}, mgrOpts)
			if err != nil {
				t.Fatalf("Failed to setup manager: %v", err)
			}

			gotError := manager.setupControllers(ctx, mgr, logger, tc.opts...)
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error from SetupControllers (-want,+got):\n%s", diff)
			}

			if len(tc.delayedGVKs) > 0 {
				simulateDelayedIntegration(mgr, tc.delayedGVKs)
				for _, gvk := range tc.delayedGVKs {
					testDelayedIntegration(&manager, gvk.Group+"/"+strings.ToLower(gvk.Kind))
				}
			}

			diff := cmp.Diff(tc.wantEnabledIntegrations, manager.getEnabledIntegrations().SortedList())
			if len(diff) != 0 {
				t.Errorf("Unexpected enabled integrations (-want,+got):\n%s", diff)
			}
		})
	}
}

type TestRESTMapper struct {
	*apimeta.DefaultRESTMapper
	lock sync.RWMutex
}

func (m *TestRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*apimeta.RESTMapping, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.DefaultRESTMapper.RESTMapping(gk, versions...)
}

// Simulates the delayed availability of GVKs
func simulateDelayedIntegration(mgr ctrlmgr.Manager, delayedGVKs []*schema.GroupVersionKind) {
	mapper := mgr.GetRESTMapper().(*TestRESTMapper)
	mapper.lock.Lock()
	defer mapper.lock.Unlock()

	for _, gvk := range delayedGVKs {
		mapper.Add(*gvk, apimeta.RESTScopeNamespace)
	}
}

func testDelayedIntegration(manager *integrationManager, crdName string) {
	for {
		_, ok := manager.getEnabledIntegrations()[crdName]
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSetupIndexes(t *testing.T) {
	testNamespace := "test"

	cases := map[string]struct {
		opts                  []Option
		workloads             []kueue.Workload
		filter                client.ListOption
		wantError             error
		wantFieldMatcherError bool
		wantWorkloads         []string
	}{
		"proper indexes are set": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("alpha-wl", testNamespace).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "alpha", "job").
					Obj(),
				*utiltesting.MakeWorkload("beta-wl", testNamespace).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "beta", "job").
					Obj(),
			},
			opts: []Option{
				WithEnabledFrameworks([]string{"batch/job"}),
			},
			filter:        client.MatchingFields{GetOwnerKey(batchv1.SchemeGroupVersion.WithKind("Job")): "alpha"},
			wantWorkloads: []string{"alpha-wl"},
		},
		"kubeflow.org/mpijob is disabled in the configAPI": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("alpha-wl", testNamespace).
					ControllerReference(kfmpi.SchemeGroupVersionKind, "alpha", "mpijob").
					Obj(),
				*utiltesting.MakeWorkload("beta-wl", testNamespace).
					ControllerReference(kfmpi.SchemeGroupVersionKind, "beta", "mpijob").
					Obj(),
			},
			opts: []Option{
				WithEnabledFrameworks([]string{"batch/job"}),
			},
			filter:                client.MatchingFields{GetOwnerKey(kfmpi.SchemeGroupVersionKind): "alpha"},
			wantFieldMatcherError: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			builder := utiltesting.NewClientBuilder().WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})
			gotIndexerErr := SetupIndexes(ctx, utiltesting.AsIndexer(builder), tc.opts...)
			if diff := cmp.Diff(tc.wantError, gotIndexerErr, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Fatalf("Unexpected setupIndexer error (-want,+got):\n%s", diff)
			}
			k8sClient := builder.Build()
			for _, wl := range tc.workloads {
				if err := k8sClient.Create(ctx, &wl); err != nil {
					t.Fatalf("Unable to create workload, %q: %v", klog.KObj(&wl), err)
				}
			}

			// In any case, a list operation without fieldMatcher should succeed.
			gotWls := &kueue.WorkloadList{}
			if gotListErr := k8sClient.List(ctx, gotWls, client.InNamespace(testNamespace)); gotListErr != nil {
				t.Fatalf("Failed to list workloads without a fieldMatcher: %v", gotListErr)
			}
			deployedWlNames := slices.Map(tc.workloads, func(j *kueue.Workload) string { return j.Name })
			gotWlNames := slices.Map(gotWls.Items, func(j *kueue.Workload) string { return j.Name })
			if diff := cmp.Diff(deployedWlNames, gotWlNames, cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b string) bool { return a < b })); len(diff) != 0 {
				t.Errorf("Unexpected list workloads (-want,+got):\n%s", diff)
			}

			// List workloads with fieldMatcher.
			gotListErr := k8sClient.List(ctx, gotWls, client.InNamespace(testNamespace), tc.filter)
			if (gotListErr != nil) != tc.wantFieldMatcherError {
				t.Errorf("Unexpected list error\nwant: %v\ngot: %v", tc.wantFieldMatcherError, gotListErr)
			}

			if !tc.wantFieldMatcherError {
				gotWlNames = slices.Map(gotWls.Items, func(j *kueue.Workload) string { return j.Name })
				if diff := cmp.Diff(tc.wantWorkloads, gotWlNames, cmpopts.EquateEmpty(),
					cmpopts.SortSlices(func(a, b string) bool { return a < b })); len(diff) != 0 {
					t.Errorf("Unexpected list workloads (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
