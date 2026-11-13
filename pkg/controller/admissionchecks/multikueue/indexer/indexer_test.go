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

package indexer

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

const (
	testConfigNamespace    = "kueue-system"
	testProviderConfigKind = "MultiKueueCapacityProviderConfig"
	testOtherController    = "example.com/other"
)

func TestSetup(t *testing.T) {
	cases := map[string]struct {
		enableClusterProfile   bool
		enableCapacityProvider bool
		objs                   []client.Object
		list                   client.ObjectList
		filter                 client.MatchingFields
		wantList               []string
		// wantListErr means that the index is not set up.
		wantListErr bool
	}{
		"MultiKueueClusters by kubeconfig": {
			objs: []client.Object{
				utiltestingapi.MakeMultiKueueCluster("cluster1").KubeConfig(kueue.SecretLocationType, "secret1").Obj(),
				utiltestingapi.MakeMultiKueueCluster("cluster2").KubeConfig(kueue.SecretLocationType, "secret2").Obj(),
			},
			list:     &kueue.MultiKueueClusterList{},
			filter:   client.MatchingFields{UsingKubeConfigs: testConfigNamespace + "/secret1"},
			wantList: []string{"cluster1"},
		},
		"MultiKueueClusters by kubeconfig, no match": {
			objs: []client.Object{
				utiltestingapi.MakeMultiKueueCluster("cluster2").KubeConfig(kueue.SecretLocationType, "secret2").Obj(),
			},
			list:   &kueue.MultiKueueClusterList{},
			filter: client.MatchingFields{UsingKubeConfigs: testConfigNamespace + "/secret1"},
		},
		"MultiKueueClusters by ClusterProfile": {
			enableClusterProfile: true,
			objs: []client.Object{
				utiltestingapi.MakeMultiKueueCluster("cluster1").ClusterProfile("clusterprofile1").Obj(),
				utiltestingapi.MakeMultiKueueCluster("cluster2").ClusterProfile("clusterprofile2").Obj(),
			},
			list:     &kueue.MultiKueueClusterList{},
			filter:   client.MatchingFields{UsingClusterProfiles: testConfigNamespace + "/clusterprofile1"},
			wantList: []string{"cluster1"},
		},
		"MultiKueueClusters by ClusterProfile, feature gate disabled": {
			list:        &kueue.MultiKueueClusterList{},
			filter:      client.MatchingFields{UsingClusterProfiles: testConfigNamespace + "/clusterprofile1"},
			wantListErr: true,
		},
		"MultiKueueConfigs by MultiKueueCluster": {
			objs: []client.Object{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj(),
				utiltestingapi.MakeMultiKueueConfig("config2").Clusters("cluster2").Obj(),
			},
			list:     &kueue.MultiKueueConfigList{},
			filter:   client.MatchingFields{UsingMultiKueueClusters: "cluster1"},
			wantList: []string{"config1"},
		},
		"MultiKueueConfigs by MultiKueueCluster, no match": {
			objs: []client.Object{
				utiltestingapi.MakeMultiKueueConfig("config2").Clusters("cluster2").Obj(),
			},
			list:   &kueue.MultiKueueConfigList{},
			filter: client.MatchingFields{UsingMultiKueueClusters: "cluster1"},
		},
		"AdmissionChecks by MultiKueueConfig": {
			objs: []client.Object{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
				utiltestingapi.MakeAdmissionCheck("other-controller").
					ControllerName(testOtherController).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
				utiltestingapi.MakeAdmissionCheck("other-kind").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "OtherKind", "config1").
					Obj(),
				utiltestingapi.MakeAdmissionCheck("no-parameters").
					ControllerName(kueue.MultiKueueControllerName).
					Obj(),
			},
			list:     &kueue.AdmissionCheckList{},
			filter:   client.MatchingFields{AdmissionCheckUsingConfigKey: "config1"},
			wantList: []string{"ac1"},
		},
		"Workloads by AdmissionCheck": {
			objs: []client.Object{
				utiltestingapi.MakeWorkload("wl1", "ns").
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
				utiltestingapi.MakeWorkload("wl2", "ns").
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac2", State: kueue.CheckStatePending}).
					Obj(),
			},
			list:     &kueue.WorkloadList{},
			filter:   client.MatchingFields{WorkloadsWithAdmissionCheckKey: "ac1"},
			wantList: []string{"wl1"},
		},
		"AdmissionChecks by controllerName": {
			enableCapacityProvider: true,
			objs: []client.Object{
				utiltestingapi.MakeAdmissionCheck("ac1").ControllerName(kueue.MultiKueueControllerName).Obj(),
				utiltestingapi.MakeAdmissionCheck("ac2").ControllerName(testOtherController).Obj(),
			},
			list:     &kueue.AdmissionCheckList{},
			filter:   client.MatchingFields{AdmissionCheckControllerNameKey: kueue.MultiKueueControllerName},
			wantList: []string{"ac1"},
		},
		"AdmissionChecks by controllerName, feature gate disabled": {
			list:        &kueue.AdmissionCheckList{},
			filter:      client.MatchingFields{AdmissionCheckControllerNameKey: kueue.MultiKueueControllerName},
			wantListErr: true,
		},
		"ClusterQueues by AdmissionCheck": {
			enableCapacityProvider: true,
			objs: []client.Object{
				utiltestingapi.MakeClusterQueue("cq1").AdmissionChecks("ac1", "ac2").Obj(),
				utiltestingapi.MakeClusterQueue("cq2").AdmissionChecks("ac2").Obj(),
				utiltestingapi.MakeClusterQueue("cq3").Obj(),
			},
			list:     &kueue.ClusterQueueList{},
			filter:   client.MatchingFields{ClusterQueueAdmissionChecksKey: "ac1"},
			wantList: []string{"cq1"},
		},
		"ClusterQueues by AdmissionCheck, feature gate disabled": {
			list:        &kueue.ClusterQueueList{},
			filter:      client.MatchingFields{ClusterQueueAdmissionChecksKey: "ac1"},
			wantListErr: true,
		},
		"MultiKueue CapacityProviders by parameters": {
			enableCapacityProvider: true,
			objs: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "config1").
					Obj(),
				utiltestingalpha.MakeCapacityProvider("other-controller").
					ControllerName(testOtherController).
					Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "config1").
					Obj(),
				utiltestingalpha.MakeCapacityProvider("other-group").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters("example.com", testProviderConfigKind, "config1").
					Obj(),
				utiltestingalpha.MakeCapacityProvider("other-kind").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueuealpha.SchemeGroupVersion.Group, "OtherKind", "config1").
					Obj(),
				utiltestingalpha.MakeCapacityProvider("no-parameters").
					ControllerName(kueue.MultiKueueControllerName).
					Obj(),
			},
			list:     &kueuealpha.CapacityProviderList{},
			filter:   client.MatchingFields{CapacityProviderParametersKey: "config1"},
			wantList: []string{"cp1"},
		},
		"MultiKueueCapacityProviderConfigs by manager ClusterQueue": {
			enableCapacityProvider: true,
			objs: []client.Object{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("config1").
					ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").
					Obj(),
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("config2").
					ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq2").
					Obj(),
				// Only ClusterQueue subtree roots are indexed in Alpha.
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cohort-config").
					ManagerSubtreeRootRef(kueuealpha.CohortSubtreeRootRefKind, "cq1").
					Obj(),
			},
			list:     &kueuealpha.MultiKueueCapacityProviderConfigList{},
			filter:   client.MatchingFields{ProviderConfigManagerSubtreeRootKey: "cq1"},
			wantList: []string{"config1"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueClusterProfile, tc.enableClusterProfile)
			features.SetFeatureGateDuringTest(t, features.DQOMultiKueueCapacityProvider, tc.enableCapacityProvider)
			ctx, _ := utiltesting.ContextWithLog(t)

			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(kueue.AddToScheme(scheme))
			utilruntime.Must(kueuealpha.AddToScheme(scheme))
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objs...)
			// The Workload index on status.admissionChecks is set up by the core indexer.
			if err := coreindexer.Setup(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatalf("Failed to set up the core indexes: %v", err)
			}
			if err := Setup(ctx, utiltesting.AsIndexer(builder), testConfigNamespace); err != nil {
				t.Fatalf("Failed to set up indexes: %v", err)
			}
			c := builder.Build()

			err := c.List(ctx, tc.list, tc.filter)
			if gotListErr := err != nil; gotListErr != tc.wantListErr {
				t.Fatalf("Unexpected List() error, want error: %v, got: %v", tc.wantListErr, err)
			}
			if tc.wantListErr {
				return
			}
			items, err := apimeta.ExtractList(tc.list)
			if err != nil {
				t.Fatalf("Failed to extract the list items: %v", err)
			}
			var gotList []string
			for _, item := range items {
				obj, ok := item.(client.Object)
				if !ok {
					t.Fatalf("Unexpected list item type %T", item)
				}
				gotList = append(gotList, obj.GetName())
			}
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("Unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}
