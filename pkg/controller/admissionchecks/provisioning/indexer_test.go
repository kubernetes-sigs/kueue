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

package provisioning

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	TestNamespace = "ns"
)

func getClientBuilder() (*fake.ClientBuilder, context.Context) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(autoscaling.AddToScheme(scheme))

	ctx := context.Background()
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: TestNamespace,
		},
	})
	_ = SetupIndexer(ctx, utiltesting.AsIndexer(builder))
	return builder, ctx
}

func TestIndexProvisioningRequests(t *testing.T) {
	cases := map[string]struct {
		requests      []*autoscaling.ProvisioningRequest
		filter        client.ListOption
		wantListError error
		wantList      []string
	}{
		"no owner": {
			requests: []*autoscaling.ProvisioningRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name",
					},
				},
			},
			filter: client.MatchingFields{RequestsOwnedByWorkloadKey: "wl"},
		},
		"single owner, single match": {
			requests: []*autoscaling.ProvisioningRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name2",
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "wl",
							},
						},
					},
				},
			},
			filter:   client.MatchingFields{RequestsOwnedByWorkloadKey: "wl"},
			wantList: []string{"name2"},
		},
		"multiple owners, multiple matches": {
			requests: []*autoscaling.ProvisioningRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name2",
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "wl",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name3",
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "wl_2",
							},
							{
								Name: "wl",
							},
						},
					},
				},
			},
			filter:   client.MatchingFields{RequestsOwnedByWorkloadKey: "wl"},
			wantList: []string{"name2", "name3"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			k8sclient := builder.Build()
			for _, req := range tc.requests {
				if err := k8sclient.Create(ctx, req); err != nil {
					t.Errorf("Unable to create %s request: %v", client.ObjectKeyFromObject(req), err)
				}
			}

			lst := &autoscaling.ProvisioningRequestList{}

			gotListErr := k8sclient.List(ctx, lst, client.InNamespace("default"), tc.filter)
			if diff := cmp.Diff(tc.wantListError, gotListErr); diff != "" {
				t.Errorf("unexpected list error (-want/+got):\n%s", diff)
			}

			gotList := slices.Map(lst.Items, func(pr *autoscaling.ProvisioningRequest) string { return pr.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestIndexWorkload(t *testing.T) {
	cases := map[string]struct {
		workloads     []*kueue.Workload
		filter        client.ListOption
		wantListError error
		wantList      []string
	}{
		"no checks": {
			workloads: []*kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name",
					},
				},
			},
			filter: client.MatchingFields{WorkloadsWithAdmissionCheckKey: "check"},
		},
		"single check, single match": {
			workloads: []*kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name2",
					},
					Status: kueue.WorkloadStatus{
						AdmissionChecks: []kueue.AdmissionCheckState{
							{
								Name: "check",
							},
						},
					},
				},
			},
			filter:   client.MatchingFields{WorkloadsWithAdmissionCheckKey: "check"},
			wantList: []string{"name2"},
		},
		"multiple checks, multiple matches": {
			workloads: []*kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name2",
					},
					Status: kueue.WorkloadStatus{
						AdmissionChecks: []kueue.AdmissionCheckState{
							{
								Name: "check",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name3",
					},
					Status: kueue.WorkloadStatus{
						AdmissionChecks: []kueue.AdmissionCheckState{
							{
								Name: "check2",
							},
							{
								Name: "check",
							},
						},
					},
				},
			},
			filter:   client.MatchingFields{WorkloadsWithAdmissionCheckKey: "check"},
			wantList: []string{"name2", "name3"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			k8sclient := builder.Build()
			for _, wl := range tc.workloads {
				if err := k8sclient.Create(ctx, wl); err != nil {
					t.Errorf("Unable to create %s workload: %v", client.ObjectKeyFromObject(wl), err)
				}
			}

			lst := &kueue.WorkloadList{}

			gotListErr := k8sclient.List(ctx, lst, client.InNamespace("default"), tc.filter)
			if diff := cmp.Diff(tc.wantListError, gotListErr); diff != "" {
				t.Errorf("unexpected list error (-want/+got):\n%s", diff)
			}

			gotList := slices.Map(lst.Items, func(wl *kueue.Workload) string { return wl.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestIndexAdmissionChecks(t *testing.T) {
	cases := map[string]struct {
		checks   []*kueue.AdmissionCheck
		filter   client.ListOption
		wantList []string
	}{
		"different controller": {
			checks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").
					ControllerName("other").
					Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
					Obj(),
			},
			filter: client.MatchingFields{AdmissionCheckUsingConfigKey: "config1"},
		},
		"bad ref group": {
			checks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters("core", ConfigKind, "config1").
					Obj(),
			},
			filter: client.MatchingFields{AdmissionCheckUsingConfigKey: "config1"},
		},
		"bad ref kind": {
			checks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, "kind", "config1").
					Obj(),
			},
			filter: client.MatchingFields{AdmissionCheckUsingConfigKey: "config1"},
		},
		"empty name": {
			checks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, ConfigKind, "").
					Obj(),
			},
			filter: client.MatchingFields{AdmissionCheckUsingConfigKey: ""},
		},
		"match": {
			checks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
					Obj(),
			},
			filter:   client.MatchingFields{AdmissionCheckUsingConfigKey: "config1"},
			wantList: []string{"check1"},
		},
		"multiple checks, partial match": {
			checks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
					Obj(),
				utiltesting.MakeAdmissionCheck("check2").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
					Obj(),
				utiltesting.MakeAdmissionCheck("check3").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, ConfigKind, "config2").
					Obj(),
			},
			filter:   client.MatchingFields{AdmissionCheckUsingConfigKey: "config1"},
			wantList: []string{"check1", "check2"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			k8sclient := builder.Build()
			for _, req := range tc.checks {
				if err := k8sclient.Create(ctx, req); err != nil {
					t.Errorf("Unable to create %s request: %v", client.ObjectKeyFromObject(req), err)
				}
			}

			lst := &kueue.AdmissionCheckList{}

			gotListErr := k8sclient.List(ctx, lst, tc.filter)
			if gotListErr != nil {
				t.Errorf("unexpected list error:%s", gotListErr)
			}

			gotList := slices.Map(lst.Items, func(ac *kueue.AdmissionCheck) string { return ac.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}
