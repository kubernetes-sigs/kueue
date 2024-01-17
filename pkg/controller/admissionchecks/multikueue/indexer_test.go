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

package multikueue

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	TestNamespace = "ns"
)

func getClientBuilder() (*fake.ClientBuilder, context.Context) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := kueue.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := kueuealpha.AddToScheme(scheme); err != nil {
		panic(err)
	}

	ctx := context.Background()
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: TestNamespace,
		},
	})
	_ = SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace)
	return builder, ctx
}

func TestMultikueConfigUsingKubeconfig(t *testing.T) {
	cases := map[string]struct {
		configs       []*kueuealpha.MultiKueueConfig
		filter        client.ListOption
		wantListError error
		wantList      []string
	}{
		"no clusters": {
			configs: []*kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "name",
					},
				},
			},
			filter: client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
		},
		"single cluster, single match": {
			configs: []*kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "config1",
					},
					Spec: kueuealpha.MultiKueueConfigSpec{
						Clusters: []kueuealpha.MultiKueueCluster{
							{
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location: "secret1",
								},
							},
						},
					},
				},
			},
			filter:   client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
			wantList: []string{"config1"},
		},
		"single cluster, no match": {
			configs: []*kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "config1",
					},
					Spec: kueuealpha.MultiKueueConfigSpec{
						Clusters: []kueuealpha.MultiKueueCluster{
							{
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location: "secret2",
								},
							},
						},
					},
				},
			},
			filter: client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
		},
		"multiple clusters, single match": {
			configs: []*kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "config1",
					},
					Spec: kueuealpha.MultiKueueConfigSpec{
						Clusters: []kueuealpha.MultiKueueCluster{
							{
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location: "secret0",
								},
							},
							{
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location: "secret1",
								},
							},
						},
					},
				},
			},
			filter:   client.MatchingFields{UsingKubeConfigs: TestNamespace + "/secret1"},
			wantList: []string{"config1"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			k8sclient := builder.Build()
			for _, req := range tc.configs {
				if err := k8sclient.Create(ctx, req); err != nil {
					t.Errorf("Unable to create %s request: %v", client.ObjectKeyFromObject(req), err)
				}
			}

			lst := &kueuealpha.MultiKueueConfigList{}

			gotListErr := k8sclient.List(ctx, lst, client.InNamespace("default"), tc.filter)
			if diff := cmp.Diff(tc.wantListError, gotListErr); diff != "" {
				t.Errorf("unexpected list error (-want/+got):\n%s", diff)
			}

			gotList := slices.Map(lst.Items, func(mkc *kueuealpha.MultiKueueConfig) string { return mkc.Name })
			if diff := cmp.Diff(tc.wantList, gotList, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected list (-want/+got):\n%s", diff)
			}
		})
	}
}
