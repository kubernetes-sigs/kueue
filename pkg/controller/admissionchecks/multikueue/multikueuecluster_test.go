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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

var (
	errInvalidConfig = errors.New("invalid kubeconfig")
)

func fakeClientBuilder(kubeconfig []byte, options client.Options) (client.WithWatch, error) {
	if string(kubeconfig) == "invalid" {
		return nil, errInvalidConfig
	}
	b, _ := getClientBuilder()
	return b.Build(), nil
}

func newTestClient(config string) *remoteClient {
	b, _ := getClientBuilder()
	localClient := b.Build()
	ret := &remoteClient{
		kubeconfig:  []byte(config),
		localClient: localClient,

		builderOverride: fakeClientBuilder,
	}
	ret.watchCancel = func() {
		ret.kubeconfig = []byte(string(ret.kubeconfig) + " canceled")
	}
	return ret
}

func makeTestSecret(name string, kubeconfig string) corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: TestNamespace,
		},
		Data: map[string][]byte{
			kueuealpha.MultiKueueConfigSecretKey: []byte(kubeconfig),
		},
	}
}

func TestUpdateConfig(t *testing.T) {
	cases := map[string]struct {
		reconcileFor  string
		remoteClients map[string]*remoteClient
		clusters      []kueuealpha.MultiKueueCluster
		secrets       []corev1.Secret

		wantRemoteClients map[string]*remoteClient
		wantClusters      []kueuealpha.MultiKueueCluster
	}{
		"new valid client is added": {
			reconcileFor: "worker1",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "worker1 kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Active(metav1.ConditionTrue, "Active", "Connected").Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with valid config": {
			reconcileFor: "worker1",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "worker1 kubeconfig"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Active(metav1.ConditionTrue, "Active", "Connected").Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with invalid config": {
			reconcileFor: "worker1",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "invalid"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Active(metav1.ConditionFalse, "ClientConnectionFailed", "invalid kubeconfig").Obj(),
			},
		},
		"missing cluster is removed": {
			reconcileFor: "worker2",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 kubeconfig"),
				"worker2": newTestClient("worker2 kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").Secret("w1", "worker1").Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			builder = builder.WithLists(&kueuealpha.MultiKueueClusterList{Items: tc.clusters})
			builder = builder.WithLists(&corev1.SecretList{Items: tc.secrets})
			builder = builder.WithStatusSubresource(slices.Map(tc.clusters, func(c *kueuealpha.MultiKueueCluster) client.Object { return c })...)
			c := builder.Build()

			reconciler := newClustersReconciler(c, TestNamespace)

			if len(tc.remoteClients) > 0 {
				reconciler.clients = tc.remoteClients
			}
			reconciler.builderOverride = fakeClientBuilder

			_, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor}})
			if gotErr != nil {
				t.Errorf("unexpected reconcile error: %s", gotErr)
			}

			lst := &kueuealpha.MultiKueueClusterList{}
			gotErr = c.List(ctx, lst)
			if gotErr != nil {
				t.Errorf("unexpected list clusters error: %s", gotErr)
			}

			if diff := cmp.Diff(tc.wantClusters, lst.Items, cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("unexpected clusters (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantRemoteClients, reconciler.clients, cmpopts.EquateEmpty(),
				cmp.Comparer(func(a, b remoteClient) bool { return string(a.kubeconfig) == string(b.kubeconfig) })); diff != "" {
				t.Errorf("unexpected controllers (-want/+got):\n%s", diff)
			}
		})
	}
}
