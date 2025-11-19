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

package multikueue

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

const (
	TestNamespace = "ns"
)

func getClientBuilder(ctx context.Context) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(inventoryv1alpha1.AddToScheme(scheme))

	utilruntime.Must(jobframework.ForEachIntegration(func(_ string, cb jobframework.IntegrationCallbacks) error {
		if cb.MultiKueueAdapter != nil && cb.AddToScheme != nil {
			return cb.AddToScheme(scheme)
		}
		return nil
	}))

	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(utiltesting.MakeNamespace(TestNamespace))
	_ = indexer.Setup(ctx, utiltesting.AsIndexer(builder))
	_ = SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace)
	return builder
}

func TestListMultiKueueClustersUsingKubeConfig(t *testing.T) {
	cases := map[string]struct {
		clusters      []*kueue.MultiKueueCluster
		filter        client.ListOption
		wantListError error
		wantList      []string
	}{
		"no clusters": {
			filter: client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
		},
		"single cluster, single match": {
			clusters: []*kueue.MultiKueueCluster{
				utiltestingapi.MakeMultiKueueCluster("cluster1").KubeConfig(kueue.SecretLocationType, "secret1").Obj(),
			},
			filter:   client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
			wantList: []string{"cluster1"},
		},
		"single cluster, no match": {
			clusters: []*kueue.MultiKueueCluster{
				utiltestingapi.MakeMultiKueueCluster("cluster2").KubeConfig(kueue.SecretLocationType, "secret2").Obj(),
			},
			filter: client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
		},
		"multiple clusters, single match": {
			clusters: []*kueue.MultiKueueCluster{
				utiltestingapi.MakeMultiKueueCluster("cluster1").KubeConfig(kueue.SecretLocationType, "secret1").Obj(),
				utiltestingapi.MakeMultiKueueCluster("cluster2").KubeConfig(kueue.SecretLocationType, "secret2").Obj(),
			},
			filter:   client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
			wantList: []string{"cluster1"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := getClientBuilder(ctx)
			k8sclient := builder.Build()
			for _, req := range tc.clusters {
				if err := k8sclient.Create(ctx, req); err != nil {
					t.Fatalf("Unable to create %q cluster: %v", client.ObjectKeyFromObject(req), err)
				}
			}

			lst := &kueue.MultiKueueClusterList{}

			gotListErr := k8sclient.List(ctx, lst, tc.filter)
			if diff := cmp.Diff(tc.wantListError, gotListErr); diff != "" {
				t.Errorf("unexpected list error (-want/+got):\n%s", diff)
			}

			gotList := slices.Map(lst.Items, func(mkc *kueue.MultiKueueCluster) string { return mkc.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestListMultiKueueClustersUsingClusterProfile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MultiKueueClusterProfile, true)
	cases := map[string]struct {
		clusters      []*kueue.MultiKueueCluster
		filter        client.ListOption
		wantListError error
		wantList      []string
	}{
		"no clusters": {
			filter: client.MatchingFields{UsingClusterProfiles: TestNamespace + "/ns1/clusterprofile1"},
		},
		"single cluster, single match": {
			clusters: []*kueue.MultiKueueCluster{
				utiltestingapi.MakeMultiKueueCluster("cluster1").ClusterProfile("clusterprofile1", "ns1").Obj(),
			},
			filter:   client.MatchingFields{UsingClusterProfiles: TestNamespace + "/ns1/clusterprofile1"},
			wantList: []string{"cluster1"},
		},
		"single cluster, no match": {
			clusters: []*kueue.MultiKueueCluster{
				utiltestingapi.MakeMultiKueueCluster("cluster2").ClusterProfile("clusterprofile2", "ns2").Obj(),
			},
			filter: client.MatchingFields{UsingClusterProfiles: TestNamespace + "/ns1/clusterprofile1"},
		},
		"multiple clusters, single match": {
			clusters: []*kueue.MultiKueueCluster{
				utiltestingapi.MakeMultiKueueCluster("cluster1").ClusterProfile("clusterprofile1", "ns1").Obj(),
				utiltestingapi.MakeMultiKueueCluster("cluster2").ClusterProfile("clusterprofile2", "ns2").Obj(),
			},
			filter:   client.MatchingFields{UsingClusterProfiles: TestNamespace + "/ns1/clusterprofile1"},
			wantList: []string{"cluster1"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := getClientBuilder(ctx)
			k8sclient := builder.Build()
			for _, req := range tc.clusters {
				if err := k8sclient.Create(ctx, req); err != nil {
					t.Fatalf("Unable to create %q cluster: %v", client.ObjectKeyFromObject(req), err)
				}
			}

			lst := &kueue.MultiKueueClusterList{}

			gotListErr := k8sclient.List(ctx, lst, tc.filter)
			if diff := cmp.Diff(tc.wantListError, gotListErr); diff != "" {
				t.Errorf("unexpected list error (-want/+got):\n%s", diff)
			}

			gotList := slices.Map(lst.Items, func(mkc *kueue.MultiKueueCluster) string { return mkc.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestListMultiKueueConfigsUsingMultiKueueClusters(t *testing.T) {
	cases := map[string]struct {
		configs       []*kueue.MultiKueueConfig
		filter        client.ListOption
		wantListError error
		wantList      []string
	}{
		"no configs": {
			filter: client.MatchingFields{UsingMultiKueueClusters: "cluster1"},
		},
		"single config, single match": {
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj(),
			},
			filter:   client.MatchingFields{UsingMultiKueueClusters: "cluster2"},
			wantList: []string{"config1"},
		},
		"single config, no match": {
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config2").Clusters("cluster2").Obj(),
			},
			filter: client.MatchingFields{UsingMultiKueueClusters: "cluster1"},
		},
		"multiple configs, single match": {
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj(),
				utiltestingapi.MakeMultiKueueConfig("config2").Clusters("cluster2").Obj(),
			},
			filter:   client.MatchingFields{UsingMultiKueueClusters: "cluster1"},
			wantList: []string{"config1"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := getClientBuilder(ctx)
			k8sclient := builder.Build()
			for _, config := range tc.configs {
				if err := k8sclient.Create(ctx, config); err != nil {
					t.Fatalf("Unable to create %q config: %v", client.ObjectKeyFromObject(config), err)
				}
			}

			lst := &kueue.MultiKueueConfigList{}

			gotListErr := k8sclient.List(ctx, lst, tc.filter)
			if diff := cmp.Diff(tc.wantListError, gotListErr); diff != "" {
				t.Errorf("unexpected list error (-want/+got):\n%s", diff)
			}

			gotList := slices.Map(lst.Items, func(mkc *kueue.MultiKueueConfig) string { return mkc.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestListWorkloadsWithAdmissionCheck(t *testing.T) {
	cases := map[string]struct {
		workloads     []*kueue.Workload
		filter        client.ListOption
		wantListError error
		wantList      []string
	}{
		"no workloads": {
			filter: client.MatchingFields{WorkloadsWithAdmissionCheckKey: "ac1"},
		},
		"single workload, single match": {
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("wl1", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "ac1",
						State: kueue.CheckStatePending,
					}).Obj(),
			},
			filter:   client.MatchingFields{WorkloadsWithAdmissionCheckKey: "ac1"},
			wantList: []string{"wl1"},
		},
		"single workload, no match": {
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("wl2", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "ac2",
						State: kueue.CheckStatePending,
					}).Obj(),
			},
			filter: client.MatchingFields{WorkloadsWithAdmissionCheckKey: "ac1"},
		},
		"multiple workloads, single match": {
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("wl1", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "ac1",
						State: kueue.CheckStatePending,
					}).Obj(),
				utiltestingapi.MakeWorkload("wl2", TestNamespace).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "ac2",
						State: kueue.CheckStatePending,
					}).Obj(),
			},
			filter:   client.MatchingFields{WorkloadsWithAdmissionCheckKey: "ac1"},
			wantList: []string{"wl1"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := getClientBuilder(ctx)
			k8sClient := builder.Build()
			for _, wl := range tc.workloads {
				if err := k8sClient.Create(ctx, wl); err != nil {
					t.Fatalf("Unable to create %q workload: %v", client.ObjectKeyFromObject(wl), err)
				}
			}

			lst := &kueue.WorkloadList{}

			gotListErr := k8sClient.List(ctx, lst, tc.filter)
			if diff := cmp.Diff(tc.wantListError, gotListErr); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotList := slices.Map(lst.Items, func(wl *kueue.Workload) string { return wl.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected (-want/+got):\n%s", diff)
			}
		})
	}
}
