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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

var (
	errInvalidConfig = errors.New("invalid kubeconfig")
	errCannotWatch   = errors.New("client cannot watch")
)

func fakeClientBuilder(ctx context.Context) func(*clientConfig, client.Options) (client.WithWatch, error) {
	return func(config *clientConfig, _ client.Options) (client.WithWatch, error) {
		kubeconfig := config.Kubeconfig
		if strings.Contains(string(kubeconfig), "invalid") {
			return nil, errInvalidConfig
		}
		b := getClientBuilder(ctx)
		b = b.WithInterceptorFuncs(interceptor.Funcs{
			Watch: func(ctx context.Context, client client.WithWatch, obj client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
				if strings.Contains(string(kubeconfig), "nowatch") {
					return nil, errCannotWatch
				}
				return client.Watch(ctx, obj, opts...)
			},
		})
		return b.Build(), nil
	}
}

func newTestClient(ctx context.Context, kubeconfig []byte, restConfig *rest.Config, watchCancel func()) *remoteClient {
	b := getClientBuilder(ctx)
	localClient := b.Build()
	ret := &remoteClient{
		config:      &clientConfig{Kubeconfig: kubeconfig, RestConfig: restConfig},
		localClient: localClient,
		watchCancel: watchCancel,

		builderOverride: fakeClientBuilder(ctx),
	}
	return ret
}

func setReconnectState(rc *remoteClient, a uint) *remoteClient {
	rc.failedConnAttempts = a
	rc.connecting.Store(true)
	return rc
}

func makeTestSecret(name string, kubeconfig string) corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: TestNamespace,
		},
		Data: map[string][]byte{
			kueue.MultiKueueConfigSecretKey: []byte(kubeconfig),
		},
	}
}

type testClusterProfileCreds struct {
	supportedProviders map[string]bool
}

func (t *testClusterProfileCreds) BuildConfigFromCP(clusterprofile *inventoryv1alpha1.ClusterProfile) (*rest.Config, error) {
	for _, provider := range clusterprofile.Status.CredentialProviders {
		if t.supportedProviders[provider.Name] {
			return &rest.Config{
				Host: clusterprofile.Name,
			}, nil
		}
	}
	return nil, errors.New("unsupported credential provider")
}

func makeTestClusterProfile(name string, providerName string) inventoryv1alpha1.ClusterProfile {
	return inventoryv1alpha1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: TestNamespace,
		},
		Status: inventoryv1alpha1.ClusterProfileStatus{
			CredentialProviders: []inventoryv1alpha1.CredentialProvider{
				{
					Name: providerName,
				},
			},
		},
	}
}

func kubeconfigBase(user string) *utiltesting.TestKubeconfigWrapper {
	return utiltesting.NewTestKubeConfigWrapper().
		Cluster("test", "https://10.10.10.10", []byte{'-', '-', '-', '-', '-'}).
		User(user, nil, nil).
		Context("test-context", "test", user).
		CurrentContext("test-context")
}

func testKubeconfig(user string) string {
	kubeconfig, _ := kubeconfigBase(user).
		TokenAuthInfo(user, "FAKE-TOKEN-123456").
		Build()
	return string(kubeconfig)
}

func testKubeconfigInsecure(user string, tokenFile *string) string {
	kubeconfig, _ := kubeconfigBase(user).
		TokenFileAuthInfo(user, *tokenFile).
		Build()
	return string(kubeconfig)
}

func TestUpdateConfig(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cancelCalledCount := 0
	cancelCalled := func() { cancelCalledCount++ }
	validKubeconfigLocation := filepath.Join(t.TempDir(), "worker1KubeConfig")

	cases := map[string]struct {
		reconcileFor    string
		remoteClients   map[string]*remoteClient
		clusters        []kueue.MultiKueueCluster
		secrets         []corev1.Secret
		clusterprofiles []inventoryv1alpha1.ClusterProfile
		cpCreds         clusterProfileCreds

		wantRemoteClients      map[string]*remoteClient
		wantClusters           []kueue.MultiKueueCluster
		wantRequeueAfter       time.Duration
		wantCancelCalled       int
		wantErr                error
		skipInsecureKubeconfig bool
	}{
		"new valid client is added": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfig("worker1")),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte(testKubeconfig("worker1")), nil, nil),
			},
		},
		"update client with valid secret config": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfig("worker1")),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte(testKubeconfig("worker1 old kubeconfig")), nil, cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					config: &clientConfig{
						Kubeconfig: []byte(testKubeconfig("worker1")),
					},
				},
			},
			wantCancelCalled: 1,
		},
		"update client with valid path config": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, validKubeconfigLocation).
					Generation(1).
					Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte(testKubeconfig("worker1 old kubeconfig")), nil, cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, validKubeconfigLocation).
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte(testKubeconfig("worker1")), nil, nil),
			},
			wantCancelCalled: 1,
		},
		"update client with invalid secret config": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfig("invalid")),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte("worker1 old kubeconfig"), nil, cancelCalled),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte(testKubeconfig("invalid")), nil, nil),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "invalid kubeconfig", 1).
					Generation(1).
					Obj(),
			},
			wantCancelCalled: 1,
		},
		"update client with invalid path config": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, "").
					Generation(1).
					Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte("worker1 old kubeconfig"), nil, cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, "").
					Active(metav1.ConditionFalse, "BadKubeConfig", "load client config failed: open : no such file or directory", 1).
					Generation(1).
					Obj(),
			},
			wantCancelCalled: 1,
			wantErr:          fmt.Errorf("failed to load client config, reason: BadKubeConfig, error: %w", errors.New("open : no such file or directory")),
		},
		"missing cluster is removed": {
			reconcileFor: "worker2",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte("worker1 kubeconfig"), nil, cancelCalled),
				"worker2": newTestClient(ctx, []byte("worker2 kubeconfig"), nil, cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					config: &clientConfig{
						Kubeconfig: []byte("worker1 kubeconfig"),
					},
				},
			},
			wantCancelCalled: 1,
		},
		"update client config, nowatch": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfig("nowatch")),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte("worker1 old kubeconfig"), nil, cancelCalled),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient(ctx, []byte(testKubeconfig("nowatch")), nil, nil), 1),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			wantRequeueAfter: 5 * time.Second,
			wantCancelCalled: 1,
		},
		"update client config, nowatch 3rd try": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfig("nowatch")),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient(ctx, []byte(testKubeconfig("nowatch")), nil, cancelCalled), 2),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient(ctx, []byte(testKubeconfig("nowatch")), nil, nil), 3),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			wantRequeueAfter: 20 * time.Second,
			wantCancelCalled: 1,
		},
		"failed attempts are set to 0 on successful connection": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfig("good_user")),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient(ctx, []byte(testKubeconfig("nowatch")), nil, cancelCalled), 5),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte(testKubeconfig("good_user")), nil, nil),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantCancelCalled: 1,
		},
		"failed attempts are set to 0 on config change": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfig("invalid")),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient(ctx, []byte("nowatch"), nil, cancelCalled), 5),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte(testKubeconfig("invalid")), nil, nil),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "invalid kubeconfig", 1).
					Generation(1).
					Obj(),
			},
			wantCancelCalled: 1,
		},
		"failed due to insecure kubeconfig": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfigInsecure("worker1", ptr.To("/path/to/tokenfile"))),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "InsecureKubeConfig", "load client config failed: tokenFile is not allowed", 1).
					Generation(1).
					Obj(),
			},
			wantErr: fmt.Errorf("failed to load client config, reason: InsecureKubeConfig, error: %w", errors.New("tokenFile is not allowed")),
		},
		"remove client with invalid kubeconfig": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfigInsecure("worker1", ptr.To("/path/to/tokenfile"))),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte("worker1 old kubeconfig"), nil, cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "InsecureKubeConfig", "load client config failed: tokenFile is not allowed", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{},
			wantCancelCalled:  1,
			wantErr:           fmt.Errorf("failed to load client config, reason: InsecureKubeConfig, error: %w", errors.New("tokenFile is not allowed")),
		},
		"skip insecure kubeconfig validation": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", testKubeconfigInsecure("worker1", ptr.To("/path/to/tokenfile"))),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					config: &clientConfig{
						Kubeconfig: []byte(testKubeconfigInsecure("worker1", ptr.To("/path/to/tokenfile"))),
					},
				},
			},
			skipInsecureKubeconfig: true,
		},
		"use cluster profile": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			clusterprofiles: []inventoryv1alpha1.ClusterProfile{
				makeTestClusterProfile("worker1", "credentialProvider1"),
			},
			cpCreds: &testClusterProfileCreds{
				supportedProviders: map[string]bool{
					"credentialProvider1": true,
				},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					config: &clientConfig{
						RestConfig: &rest.Config{Host: "worker1"},
					},
				},
			},
		},
		"unsupported credential provider": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			clusterprofiles: []inventoryv1alpha1.ClusterProfile{
				makeTestClusterProfile("worker1", "credentialProvider1"),
			},
			cpCreds: &testClusterProfileCreds{
				supportedProviders: map[string]bool{},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Active(metav1.ConditionFalse, "BadClusterProfile", "load client config failed: unsupported credential provider", 1).
					Generation(1).
					Obj(),
			},
			wantErr: fmt.Errorf("failed to load client config, reason: BadClusterProfile, error: %w", errors.New("unsupported credential provider")),
		},
		"cluster profile not found": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			clusterprofiles: []inventoryv1alpha1.ClusterProfile{
				makeTestClusterProfile("worker2", "credentialProvider2"),
			},
			cpCreds: &testClusterProfileCreds{
				supportedProviders: map[string]bool{},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Active(metav1.ConditionFalse, "BadClusterProfile", "load client config failed: clusterprofiles.multicluster.x-k8s.io \"worker1\" not found", 1).
					Generation(1).
					Obj(),
			},
			wantErr: fmt.Errorf("failed to load client config, reason: BadClusterProfile, error: %w", errors.New(`clusterprofiles.multicluster.x-k8s.io "worker1" not found`)),
		},
		"cluster profile feature gate disabled": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			cpCreds: &testClusterProfileCreds{
				supportedProviders: map[string]bool{},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1", TestNamespace).
					Active(metav1.ConditionFalse, "MultiKueueClusterProfileFeatureDisabled", "load client config failed: MultiKueueClusterProfile feature gate is disabled", 1).
					Generation(1).
					Obj(),
			},
			wantErr: fmt.Errorf("failed to load client config, reason: MultiKueueClusterProfileFeatureDisabled, error: %w", errors.New("MultiKueueClusterProfile feature gate is disabled")),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := getClientBuilder(ctx)
			builder = builder.WithLists(&kueue.MultiKueueClusterList{Items: tc.clusters})
			builder = builder.WithLists(&corev1.SecretList{Items: tc.secrets})
			builder = builder.WithLists(&inventoryv1alpha1.ClusterProfileList{Items: tc.clusterprofiles})
			builder = builder.WithStatusSubresource(slices.Map(tc.clusters, func(c *kueue.MultiKueueCluster) client.Object { return c })...)
			c := builder.Build()

			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
			reconciler := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, tc.cpCreds)

			reconciler.rootContext = ctx

			if len(tc.remoteClients) > 0 {
				reconciler.remoteClients = tc.remoteClients
			}
			reconciler.builderOverride = fakeClientBuilder(ctx)

			if tc.skipInsecureKubeconfig {
				features.SetFeatureGateDuringTest(t, features.MultiKueueAllowInsecureKubeconfigs, true)
			}

			if len(tc.clusterprofiles) > 0 {
				features.SetFeatureGateDuringTest(t, features.MultiKueueClusterProfile, true)
			}

			// Create test kubeconfig file for path location type
			if tc.clusters != nil && tc.clusters[0].Spec.KubeConfig != nil && tc.clusters[0].Spec.KubeConfig.LocationType == kueue.PathLocationType && tc.clusters[0].Spec.KubeConfig.Location != "" {
				kubeconfigBytes := testKubeconfig("worker1")
				if err := os.WriteFile(tc.clusters[0].Spec.KubeConfig.Location, []byte(kubeconfigBytes), 0666); err != nil {
					t.Errorf("Failed to create test file (%s): %v", tc.clusters[0].Spec.KubeConfig.Location, err)
				}
			}

			cancelCalledCount = 0
			res, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor}})
			if diff := cmp.Diff(gotErr, tc.wantErr, cmp.Comparer(func(a, b error) bool {
				if a == nil || b == nil {
					return a == b
				}
				return a.Error() == b.Error()
			})); diff != "" {
				t.Errorf("unexpected reconcile error: \nwant:\n%v\ngot:%v\n", tc.wantErr, gotErr)
			}

			if diff := cmp.Diff(tc.wantRequeueAfter, res.RequeueAfter); diff != "" {
				t.Errorf("unexpected requeue after (-want/+got):\n%s", diff)
			}

			if tc.wantCancelCalled != cancelCalledCount {
				t.Errorf("unexpected watch cancel call count want: %d,  got: %d", tc.wantCancelCalled, cancelCalledCount)
			}

			lst := &kueue.MultiKueueClusterList{}
			gotErr = c.List(ctx, lst)
			if gotErr != nil {
				t.Errorf("unexpected list clusters error: %s", gotErr)
			}

			if diff := cmp.Diff(tc.wantClusters, lst.Items, cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("unexpected clusters (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantRemoteClients, reconciler.remoteClients, cmpopts.EquateEmpty(),
				cmp.Comparer(func(a, b *remoteClient) bool {
					if a.failedConnAttempts != b.failedConnAttempts {
						return false
					}
					if a.config == nil || b.config == nil {
						return a.config == b.config
					}
					if string(a.config.Kubeconfig) != string(b.config.Kubeconfig) {
						return false
					}
					if cmp.Diff(a.config.RestConfig, b.config.RestConfig) != "" {
						return false
					}
					return true
				})); diff != "" {
				t.Errorf("unexpected controllers (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestRemoteClientGC(t *testing.T) {
	baseJobBuilder := testingjob.MakeJob("job1", TestNamespace)
	baseWlBuilder := utiltestingapi.MakeWorkload("wl1", TestNamespace).ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "test-uuid")

	cases := map[string]struct {
		managersWorkloads []kueue.Workload
		workersWorkloads  []kueue.Workload
		managersJobs      []batchv1.Job
		workersJobs       []batchv1.Job

		wantWorkersWorkloads []kueue.Workload
		wantWorkersJobs      []batchv1.Job
	}{
		"existing workers and jobs are not deleted": {
			managersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Obj(),
			},
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
			workersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
			wantWorkersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantWorkersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
		},
		"missing worker workloads are deleted": {
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
		},
		"missing worker workloads are deleted (no job adapter)": {
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("NptAJob"), "job1", "test-uuid").
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"missing worker workloads and their owner jobs are deleted": {
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
			workersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
		},
		"unrelated workers and jobs are not deleted": {
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, "other-gc-key").
					Obj(),
			},
			workersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
			wantWorkersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, "other-gc-key").
					Obj(),
			},
			wantWorkersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
		},
	}

	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			manageBuilder := getClientBuilder(ctx)
			manageBuilder = manageBuilder.WithLists(&kueue.WorkloadList{Items: tc.managersWorkloads}, &batchv1.JobList{Items: tc.managersJobs})
			managerClient := manageBuilder.Build()

			worker1Builder := getClientBuilder(ctx)
			worker1Builder = worker1Builder.WithLists(&kueue.WorkloadList{Items: tc.workersWorkloads}, &batchv1.JobList{Items: tc.workersJobs})
			worker1Client := worker1Builder.Build()

			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
			w1remoteClient := newRemoteClient(managerClient, nil, nil, defaultOrigin, "", adapters)
			w1remoteClient.client = worker1Client
			w1remoteClient.connecting.Store(false)

			w1remoteClient.runGC(ctx)

			gotWorker1Workloads := &kueue.WorkloadList{}
			err := worker1Client.List(ctx, gotWorker1Workloads)
			if err != nil {
				t.Error("unexpected list worker's workloads error")
			}

			if diff := cmp.Diff(tc.wantWorkersWorkloads, gotWorker1Workloads.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected worker's workloads (-want/+got):\n%s", diff)
			}

			gotWorker1Job := &batchv1.JobList{}
			err = worker1Client.List(ctx, gotWorker1Job)
			if err != nil {
				t.Error("unexpected list worker's jobs error")
			}

			if diff := cmp.Diff(tc.wantWorkersJobs, gotWorker1Job.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected worker's jobs (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestValidateKubeconfig(t *testing.T) {
	kubeconfigBase := utiltesting.NewTestKubeConfigWrapper().Cluster("test", "https://10.10.10.10", []byte{0x2d, 0x2d, 0x2d, 0x2d, 0x2d}).
		User("u", nil, nil).
		Context("test-context", "test", "u").
		CurrentContext("test-context")

	cases := map[string]struct {
		cfgFn   func() *clientcmdapi.Config
		wantErr bool
	}{
		"tokenFile not allowed": {
			cfgFn: func() *clientcmdapi.Config {
				c := kubeconfigBase.Clone().TokenFileAuthInfo("u", "/tmp/tokenfile").Obj()
				return &c
			},
			wantErr: true,
		},
		"insecure skip-tls": {
			cfgFn: func() *clientcmdapi.Config {
				c := kubeconfigBase.Clone().InsecureSkipTLSVerify("test", true).Obj()
				return &c
			},
			wantErr: true,
		},
		"certificate-authority file disallowed": {
			cfgFn: func() *clientcmdapi.Config {
				c := kubeconfigBase.Clone().CAFileCluster("test", "/tmp/ca").Obj()
				return &c
			},
			wantErr: true,
		},
		"valid config": {
			cfgFn: func() *clientcmdapi.Config {
				c := kubeconfigBase.Clone().Obj()
				return &c
			},
			wantErr: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := tc.cfgFn()
			raw, _ := clientcmd.Write(*c)
			err := validateKubeconfig(raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v, got err: %v", tc.wantErr, err)
			}
		})
	}
}
