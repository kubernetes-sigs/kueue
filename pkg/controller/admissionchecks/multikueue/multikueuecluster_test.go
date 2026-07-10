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
	"sync"
	"sync/atomic"
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
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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

func fakeClientBuilder(ctx context.Context) func(context.Context, *clientConfig, client.Options) (SelectivelyCachingClient, error) {
	return func(builderCtx context.Context, config *clientConfig, options client.Options) (SelectivelyCachingClient, error) {
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
		return NewNeverCachingClient(b.Build()), nil
	}
}

func newTestClient(ctx context.Context, kubeconfig []byte, restConfig *rest.Config, watchCancel func()) *remoteClient {
	b := getClientBuilder(ctx)
	localClient := b.Build()
	ret := &remoteClient{
		config:      &clientConfig{Kubeconfig: kubeconfig, RestConfig: restConfig},
		localClient: localClient,
		watchCancel: watchCancel,
		clock:       clock.RealClock{},

		builderOverride: fakeClientBuilder(ctx),
	}
	return ret
}

func setReconnectState(rc *remoteClient, a uint) *remoteClient {
	rc.failedConnAttempts = a
	rc.connected.Store(false)
	return rc
}

func makeTestSecret(name string, kubeconfig string) corev1.Secret {
	return *utiltesting.MakeSecret(name, TestNamespace).Data(kueue.MultiKueueConfigSecretKey, []byte(kubeconfig)).Obj()
}

type testClusterProfileAccessProvider struct {
	supportedProviders map[string]bool
}

func (t *testClusterProfileAccessProvider) BuildConfigFromCP(clusterprofile *inventoryv1alpha1.ClusterProfile) (*rest.Config, error) {
	for _, provider := range clusterprofile.Status.AccessProviders {
		if t.supportedProviders[provider.Name] {
			if strings.Contains(clusterprofile.Name, "invalid") {
				return testRestConfigInvalid(), nil
			}
			return testRestConfig(), nil
		}
	}
	return nil, errors.New("unsupported access provider")
}

func testRestConfig() *rest.Config {
	return &rest.Config{
		Host: "https://10.10.10.10",
		ExecProvider: &clientcmdapi.ExecConfig{
			APIVersion: "v1",
			Command:    "/test-command",
		},
	}
}

func testRestConfigInvalid() *rest.Config {
	return &rest.Config{
		Host:            "invalid-host",
		BearerTokenFile: "/path/to/file",
	}
}

func makeTestClusterProfile(name string, providerName string) inventoryv1alpha1.ClusterProfile {
	return inventoryv1alpha1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: TestNamespace,
		},
		Status: inventoryv1alpha1.ClusterProfileStatus{
			AccessProviders: []inventoryv1alpha1.AccessProvider{
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
		reconcileFor     string
		remoteClients    map[string]*remoteClient
		clusters         []kueue.MultiKueueCluster
		secrets          []corev1.Secret
		clusterprofiles  []inventoryv1alpha1.ClusterProfile
		cpAccessProvider clusterProfileAccessProvider

		wantRemoteClients             map[string]*remoteClient
		wantClusters                  []kueue.MultiKueueCluster
		wantRequeueAfter              time.Duration
		wantCancelCalled              int
		wantErr                       error
		wantEvents                    []utiltesting.EventRecord
		overrideKubeConfigPrefix      bool
		multiKueueSafePathFeatureGate bool
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
			wantCancelCalled:              1,
			overrideKubeConfigPrefix:      true,
			multiKueueSafePathFeatureGate: true,
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
				"worker1": setReconnectState(newTestClient(ctx, []byte(testKubeconfig("invalid")), nil, nil), 1),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "invalid kubeconfig", 1).
					Generation(1).
					Obj(),
			},
			wantRequeueAfter: 5 * time.Second,
			wantCancelCalled: 1,
		},
		"update client with invalid path config": {
			reconcileFor:                  "worker1",
			multiKueueSafePathFeatureGate: true,
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
					Active(metav1.ConditionFalse, "BadKubeConfig", "load client config failed: kubeconfig path must not be empty", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte("worker1 old kubeconfig"), nil, nil),
			},
			wantCancelCalled: 1,
			wantErr:          fmt.Errorf("failed to load client config, reason: BadKubeConfig, error: %w", errors.New("kubeconfig path must not be empty")),
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
				"worker1": setReconnectState(newTestClient(ctx, []byte(testKubeconfig("invalid")), nil, nil), 1),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "invalid kubeconfig", 1).
					Generation(1).
					Obj(),
			},
			wantRequeueAfter: 5 * time.Second,
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
				makeTestSecret("worker1", testKubeconfigInsecure("worker1", new("/path/to/tokenfile"))),
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
				makeTestSecret("worker1", testKubeconfigInsecure("worker1", new("/path/to/tokenfile"))),
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
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient(ctx, []byte("worker1 old kubeconfig"), nil, nil),
			},
			wantCancelCalled: 1,
			wantErr:          fmt.Errorf("failed to load client config, reason: InsecureKubeConfig, error: %w", errors.New("tokenFile is not allowed")),
		},
		"use cluster profile": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			clusterprofiles: []inventoryv1alpha1.ClusterProfile{
				makeTestClusterProfile("worker1", "accessProvider1"),
			},
			cpAccessProvider: &testClusterProfileAccessProvider{
				supportedProviders: map[string]bool{
					"accessProvider1": true,
				},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					config: &clientConfig{
						RestConfig: testRestConfig(),
					},
				},
			},
		},
		"unsupported access provider": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			clusterprofiles: []inventoryv1alpha1.ClusterProfile{
				makeTestClusterProfile("worker1", "accessProvider1"),
			},
			cpAccessProvider: &testClusterProfileAccessProvider{
				supportedProviders: map[string]bool{},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Active(metav1.ConditionFalse, "BadClusterProfile", "load client config failed: unsupported access provider", 1).
					Generation(1).
					Obj(),
			},
			wantErr: fmt.Errorf("failed to load client config, reason: BadClusterProfile, error: %w", errors.New("unsupported access provider")),
		},
		"cluster profile not found": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			clusterprofiles: []inventoryv1alpha1.ClusterProfile{
				makeTestClusterProfile("worker2", "accessProvider2"),
			},
			cpAccessProvider: &testClusterProfileAccessProvider{
				supportedProviders: map[string]bool{},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Active(metav1.ConditionFalse, "BadClusterProfile", "load client config failed: clusterprofiles.multicluster.x-k8s.io \"worker1\" not found", 1).
					Generation(1).
					Obj(),
			},
			wantErr: nil,
		},
		"cluster profile feature gate disabled": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			cpAccessProvider: &testClusterProfileAccessProvider{
				supportedProviders: map[string]bool{},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					ClusterProfile("worker1").
					Active(metav1.ConditionFalse, "MultiKueueClusterProfileFeatureDisabled", "load client config failed: MultiKueueClusterProfile feature gate is disabled", 1).
					Generation(1).
					Obj(),
			},
			wantErr: fmt.Errorf("failed to load client config, reason: MultiKueueClusterProfileFeatureDisabled, error: %w", errors.New("MultiKueueClusterProfile feature gate is disabled")),
		},
		"path with feature gate off emits deprecation warning": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, validKubeconfigLocation).
					Generation(1).
					Obj(),
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
			multiKueueSafePathFeatureGate: false,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "worker1"},
					EventType: "Warning",
					Reason:    "DeprecatedPathUsage",
					Message:   "Using locationType=Path without MultiKueueKubeConfigPathValidation feature gate is deprecated and will be removed in a future release. Enable the MultiKueueKubeConfigPathValidation feature gate and place kubeconfig files under /etc/multikueue/kubeconfigs/.",
				},
			},
		},
		"invalid rest config from cluster profile": {
			reconcileFor: "invalid",
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("invalid").
					ClusterProfile("invalid").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{},
			clusterprofiles: []inventoryv1alpha1.ClusterProfile{
				makeTestClusterProfile("invalid", "accessProvider1"),
			},
			cpAccessProvider: &testClusterProfileAccessProvider{
				supportedProviders: map[string]bool{
					"accessProvider1": true,
				},
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("invalid").
					ClusterProfile("invalid").
					Active(metav1.ConditionFalse, "BadRestConfig", "load client config failed: bearerTokenFile is not allowed", 1).
					Generation(1).
					Obj(),
			},
			wantErr: fmt.Errorf("failed to load client config, reason: BadRestConfig, error: %w", errors.New("bearerTokenFile is not allowed")),
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
			recorder := &utiltesting.EventRecorder{}
			reconciler := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, tc.cpAccessProvider, nil, recorder)

			reconciler.rootContext = ctx

			if len(tc.remoteClients) > 0 {
				reconciler.remoteClients = tc.remoteClients
			}
			reconciler.builderOverride = fakeClientBuilder(ctx)

			features.SetFeatureGateDuringTest(t, features.MultiKueueKubeConfigPathValidation, tc.multiKueueSafePathFeatureGate)

			if tc.overrideKubeConfigPrefix {
				// Override the hardcoded prefix for testing with temp dirs.
				reconciler.kubeConfigPathPrefix = filepath.Dir(validKubeconfigLocation)
			}

			if len(tc.clusterprofiles) > 0 {
				features.SetFeatureGateDuringTest(t, features.MultiKueueClusterProfile, true)
			}

			// Create test kubeconfig file for path location type
			if tc.clusters != nil && tc.clusters[0].Spec.ClusterSource.KubeConfig != nil && tc.clusters[0].Spec.ClusterSource.KubeConfig.LocationType == kueue.PathLocationType &&
				tc.clusters[0].Spec.ClusterSource.KubeConfig.Location != "" {
				kubeconfigBytes := testKubeconfig("worker1")
				if err := os.WriteFile(tc.clusters[0].Spec.ClusterSource.KubeConfig.Location, []byte(kubeconfigBytes), 0666); err != nil {
					t.Errorf("Failed to create test file (%s): %v", tc.clusters[0].Spec.ClusterSource.KubeConfig.Location, err)
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

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestReconnectBackoff(t *testing.T) {
	now := time.Now()

	type step struct {
		advance            time.Duration
		wantBuildCalls     int
		wantFailedAttempts uint
		wantRequeueAfter   time.Duration
	}

	cases := map[string]struct {
		kubeconfig string
		steps      []step
	}{
		"double reconcile within backoff window does not advance attempts": {
			kubeconfig: testKubeconfig("nowatch"),
			steps: []step{
				{advance: 0, wantBuildCalls: 1, wantFailedAttempts: 1, wantRequeueAfter: 5 * time.Second},
				{advance: 0, wantBuildCalls: 0, wantFailedAttempts: 1, wantRequeueAfter: 5 * time.Second},
				{advance: 2 * time.Second, wantBuildCalls: 0, wantFailedAttempts: 1, wantRequeueAfter: 3 * time.Second},
			},
		},
		"backoff progresses once window elapses": {
			kubeconfig: testKubeconfig("nowatch"),
			steps: []step{
				{advance: 0, wantBuildCalls: 1, wantFailedAttempts: 1, wantRequeueAfter: 5 * time.Second},
				{advance: 5 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 2, wantRequeueAfter: 10 * time.Second},
				{advance: 10 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 3, wantRequeueAfter: 20 * time.Second},
			},
		},
		"deferred reconciles between failures do not consume backoff slots": {
			kubeconfig: testKubeconfig("nowatch"),
			steps: []step{
				{advance: 0, wantBuildCalls: 1, wantFailedAttempts: 1, wantRequeueAfter: 5 * time.Second},
				{advance: 0, wantBuildCalls: 0, wantFailedAttempts: 1, wantRequeueAfter: 5 * time.Second},
				{advance: 5 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 2, wantRequeueAfter: 10 * time.Second},
				{advance: 0, wantBuildCalls: 0, wantFailedAttempts: 2, wantRequeueAfter: 10 * time.Second},
				{advance: 10 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 3, wantRequeueAfter: 20 * time.Second},
			},
		},
		"backoff saturates at retryMaxSteps": {
			kubeconfig: testKubeconfig("nowatch"),
			steps: []step{
				{advance: 0, wantBuildCalls: 1, wantFailedAttempts: 1, wantRequeueAfter: 5 * time.Second},
				{advance: 5 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 2, wantRequeueAfter: 10 * time.Second},
				{advance: 10 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 3, wantRequeueAfter: 20 * time.Second},
				{advance: 20 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 4, wantRequeueAfter: 40 * time.Second},
				{advance: 40 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 5, wantRequeueAfter: 80 * time.Second},
				{advance: 80 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 6, wantRequeueAfter: 160 * time.Second},
				{advance: 160 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 7, wantRequeueAfter: 320 * time.Second},
				{advance: 320 * time.Second, wantBuildCalls: 1, wantFailedAttempts: 8, wantRequeueAfter: 320 * time.Second},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			fc := testingclock.NewFakeClock(now)

			cluster := utiltestingapi.MakeMultiKueueCluster("worker1").
				KubeConfig(kueue.SecretLocationType, "worker1").
				Generation(1).
				Obj()
			secret := makeTestSecret("worker1", tc.kubeconfig)

			builder := getClientBuilder(ctx)
			builder = builder.WithObjects(cluster, &secret)
			builder = builder.WithStatusSubresource(&kueue.MultiKueueCluster{})
			c := builder.Build()

			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
			recorder := &utiltesting.EventRecorder{}
			reconciler := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, &testClusterProfileAccessProvider{}, nil, recorder)
			reconciler.rootContext = ctx

			var buildCalls int
			inner := fakeClientBuilder(ctx)
			reconciler.builderOverride = func(builderCtx context.Context, cfg *clientConfig, opts client.Options) (SelectivelyCachingClient, error) {
				buildCalls++
				return inner(builderCtx, cfg, opts)
			}

			rc := newRemoteClient(c, reconciler.wlUpdateCh, reconciler.watchEndedCh, reconciler.cqUpdateCh, defaultOrigin, "worker1", adapters)
			rc.clock = fc
			rc.builderOverride = reconciler.builderOverride
			reconciler.remoteClients["worker1"] = rc

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "worker1"}}

			for i, s := range tc.steps {
				fc.Step(s.advance)
				before := buildCalls
				res, err := reconciler.Reconcile(ctx, req)
				if err != nil {
					t.Fatalf("step %d: unexpected reconcile error: %v", i, err)
				}
				if got := buildCalls - before; got != s.wantBuildCalls {
					t.Errorf("step %d: builder invocations: want %d, got %d", i, s.wantBuildCalls, got)
				}
				if rc.failedConnAttempts != s.wantFailedAttempts {
					t.Errorf("step %d: failedConnAttempts: want %d, got %d", i, s.wantFailedAttempts, rc.failedConnAttempts)
				}
				if res.RequeueAfter != s.wantRequeueAfter {
					t.Errorf("step %d: RequeueAfter: want %v, got %v", i, s.wantRequeueAfter, res.RequeueAfter)
				}
			}
		})
	}
}

func TestDisconnectedClientReconnectsWithSameConfig(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	kubeconfig := testKubeconfig("worker1")

	cluster := utiltestingapi.MakeMultiKueueCluster("worker1").
		KubeConfig(kueue.SecretLocationType, "worker1").
		Active(metav1.ConditionFalse, "BadKubeConfig", "load client config failed", 1).
		Generation(1).
		Obj()
	secret := makeTestSecret("worker1", kubeconfig)

	builder := getClientBuilder(ctx)
	builder = builder.WithObjects(cluster, &secret)
	builder = builder.WithStatusSubresource(&kueue.MultiKueueCluster{})
	c := builder.Build()

	adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
	recorder := &utiltesting.EventRecorder{}
	reconciler := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, &testClusterProfileAccessProvider{}, nil, recorder)
	reconciler.rootContext = ctx

	var buildCalls int
	inner := fakeClientBuilder(ctx)
	reconciler.builderOverride = func(builderCtx context.Context, cfg *clientConfig, opts client.Options) (SelectivelyCachingClient, error) {
		buildCalls++
		return inner(builderCtx, cfg, opts)
	}

	rc := newTestClient(ctx, []byte(kubeconfig), nil, nil)
	rc.builderOverride = reconciler.builderOverride
	rc.connected.Store(false)
	reconciler.remoteClients["worker1"] = rc
	defer rc.StopWatchers()

	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "worker1"}})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("builder invocations: want 1, got %d", buildCalls)
	}
	if connected := rc.connected.Load(); !connected {
		t.Errorf("expected state to be connected")
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
				*baseWlBuilder.DeepCopy(),
			},
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobBuilder.DeepCopy(),
			},
			workersJobs: []batchv1.Job{
				*baseJobBuilder.DeepCopy(),
			},
			wantWorkersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantWorkersJobs: []batchv1.Job{
				*baseJobBuilder.DeepCopy(),
			},
		},
		"missing worker workloads are deleted": {
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			managersJobs: []batchv1.Job{
				*baseJobBuilder.DeepCopy(),
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
				*baseJobBuilder.DeepCopy(),
			},
			workersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, defaultOrigin).
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
				*baseJobBuilder.DeepCopy(),
			},
			wantWorkersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueue.MultiKueueOriginLabel, "other-gc-key").
					Obj(),
			},
			wantWorkersJobs: []batchv1.Job{
				*baseJobBuilder.DeepCopy(),
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
			worker1Client := NewNeverCachingClient(worker1Builder.Build())

			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
			w1remoteClient := newRemoteClient(managerClient, nil, nil, nil, defaultOrigin, "", adapters)
			w1remoteClient.client = worker1Client
			w1remoteClient.connected.Store(true)

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
				return kubeconfigBase.DeepCopy()
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

func TestClustersReconcilerEventFilters(t *testing.T) {
	baseCluster := utiltestingapi.MakeMultiKueueCluster("worker1").
		KubeConfig(kueue.SecretLocationType, "secret1").Generation(1).Obj()
	baseClusterWithPath := utiltestingapi.MakeMultiKueueCluster("worker1").
		KubeConfig(kueue.PathLocationType, "/tmp/worker1.kubeconfig").Generation(1).Obj()

	deletingCluster := baseCluster.DeepCopy()
	now := metav1.Now()
	deletingCluster.DeletionTimestamp = &now

	cases := map[string]struct {
		invoke        func(p predicate.Predicate) bool
		wantReconcile bool
	}{
		"create cluster with secret location triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Create(event.CreateEvent{Object: baseCluster})
			},
			wantReconcile: true,
		},
		"create cluster with path location triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Create(event.CreateEvent{Object: baseClusterWithPath})
			},
			wantReconcile: true,
		},
		"update cluster spec change triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Update(event.UpdateEvent{
					ObjectOld: baseCluster,
					ObjectNew: utiltestingapi.MakeMultiKueueCluster("worker1").
						KubeConfig(kueue.SecretLocationType, "secret2").Generation(2).Obj(),
				})
			},
			wantReconcile: true,
		},
		"update cluster status only change skips reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Update(event.UpdateEvent{
					ObjectOld: baseCluster,
					ObjectNew: utiltestingapi.MakeMultiKueueCluster("worker1").
						KubeConfig(kueue.SecretLocationType, "secret1").Generation(1).
						Active(metav1.ConditionTrue, "Active", "Connected", 1).
						Obj(),
				})
			},
			wantReconcile: false,
		},
		"update cluster deletion timestamp triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Update(event.UpdateEvent{
					ObjectOld: baseCluster,
					ObjectNew: deletingCluster,
				})
			},
			wantReconcile: true,
		},
		"update cluster no change skips reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Update(event.UpdateEvent{
					ObjectOld: baseCluster,
					ObjectNew: baseCluster.DeepCopy(),
				})
			},
			wantReconcile: false,
		},
		"update cluster path location changed to secret triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				objNew := baseCluster.DeepCopy()
				objNew.SetGeneration(2)
				return p.Update(event.UpdateEvent{
					ObjectOld: baseClusterWithPath,
					ObjectNew: objNew,
				})
			},
			wantReconcile: true,
		},
		"update non-cluster objects triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Update(event.UpdateEvent{
					ObjectOld: &corev1.Secret{},
					ObjectNew: &corev1.Secret{},
				})
			},
			wantReconcile: true,
		},
		"delete cluster with secret location triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Delete(event.DeleteEvent{Object: baseCluster})
			},
			wantReconcile: true,
		},
		"delete cluster with path location triggers reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Delete(event.DeleteEvent{Object: baseClusterWithPath})
			},
			wantReconcile: true,
		},
		"generic event does not trigger reconcile": {
			invoke: func(p predicate.Predicate) bool {
				return p.Generic(event.GenericEvent{Object: baseCluster})
			},
			wantReconcile: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			c := getClientBuilder(ctx).Build()
			recorder := &utiltesting.EventRecorder{}
			reconciler := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, newKubeConfigFSWatcher(), nil, &NoOpClusterProfileAccessProvider{}, nil, recorder)
			reconciler.rootContext = ctx

			if got := tc.invoke(reconciler); got != tc.wantReconcile {
				t.Errorf("unexpected reconcile decision: want %v, got %v", tc.wantReconcile, got)
			}
		})
	}
}

func TestEstablishWatch(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	const testTimeout = 100 * time.Millisecond
	errBoom := errors.New("boom")

	cases := map[string]struct {
		interceptor interceptor.Funcs
		wantErr     error
		maxElapsed  time.Duration
	}{
		"hung Watch times out": {
			interceptor: interceptor.Funcs{
				Watch: func(ctx context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) (watch.Interface, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
			wantErr:    errWatchEstablishTimeout,
			maxElapsed: 10 * testTimeout,
		},
		"Watch error propagates": {
			interceptor: interceptor.Funcs{
				Watch: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) (watch.Interface, error) {
					return nil, errBoom
				},
			},
			wantErr: errBoom,
		},
		"success returns without waiting": {
			maxElapsed: testTimeout,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := getClientBuilder(ctx).WithInterceptorFuncs(tc.interceptor).Build()

			start := time.Now()
			w, err := establishWatch(ctx, c, &kueue.WorkloadList{}, "test-origin", testTimeout)
			elapsed := time.Since(start)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want err %v, got: %v", tc.wantErr, err)
			}
			if (err == nil) != (w != nil) {
				t.Fatalf("watcher/err mismatch: err=%v, w=%v", err, w)
			}
			if w != nil {
				w.Stop()
			}
			if tc.maxElapsed > 0 && elapsed >= tc.maxElapsed {
				t.Fatalf("took %v, expected < %v", elapsed, tc.maxElapsed)
			}
		})
	}

	// Watch races with the timeout: returns a non-nil watcher just after
	// time.After fires. Must Stop() it to avoid leaking the stream.
	t.Run("racing watcher is stopped on timeout", func(t *testing.T) {
		fw := watch.NewFake()
		c := getClientBuilder(ctx).WithInterceptorFuncs(interceptor.Funcs{
			Watch: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) (watch.Interface, error) {
				time.Sleep(2 * testTimeout)
				return fw, nil
			},
		}).Build()

		w, err := establishWatch(ctx, c, &kueue.WorkloadList{}, "test-origin", testTimeout)
		if !errors.Is(err, errWatchEstablishTimeout) {
			t.Fatalf("want errWatchEstablishTimeout, got: %v", err)
		}
		if w != nil {
			t.Fatalf("want nil watcher, got: %v", w)
		}
		if !fw.IsStopped() {
			t.Fatal("racing watcher was not Stop()ed; would leak")
		}
	})
}

// Pins the schedule produced by establishBackoff: 1m, 2m, 4m, 8m, 10m, 10m, ...
// The helper itself is tested in pkg/util/wait; this is a guard against
// accidental changes to the initial/cap/factor wiring.
func TestEstablishBackoffSchedule(t *testing.T) {
	cases := map[string]struct {
		failedAttempts uint
		want           time.Duration
	}{
		"first attempt is initial":  {failedAttempts: 0, want: 1 * time.Minute},
		"one failure doubles":       {failedAttempts: 1, want: 2 * time.Minute},
		"two failures":              {failedAttempts: 2, want: 4 * time.Minute},
		"three failures":            {failedAttempts: 3, want: 8 * time.Minute},
		"four failures hits cap":    {failedAttempts: 4, want: maxEstablishTimeout},
		"many failures stay at cap": {failedAttempts: 20, want: maxEstablishTimeout},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := establishBackoff.WaitTime(int(tc.failedAttempts) + 1)
			if got != tc.want {
				t.Fatalf("WaitTime(%d) = %v, want %v", tc.failedAttempts+1, got, tc.want)
			}
		})
	}
}

// Regression for #11297. A remote stuck inside Watch must not stop
// reconciles of other clusters.
func TestSetRemoteClientConfigDoesNotBlockOtherClusters(t *testing.T) {
	// stuckWatchTimeout is the per-phase budget for the test: how long we
	// wait for the slow watch to be reached, for the fast reconcile to
	// finish, and for the slow goroutine to clean up after release. Generous
	// enough to survive a loaded CI runner.
	const stuckWatchTimeout = 5 * time.Second

	ctx, _ := utiltesting.ContextWithLog(t)

	slowReached := make(chan struct{})
	slowRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseSlow := func() { releaseOnce.Do(func() { close(slowRelease) }) }
	t.Cleanup(releaseSlow)

	gatedBuilder := func(_ context.Context, config *clientConfig, _ client.Options) (SelectivelyCachingClient, error) {
		kubeconfig := string(config.Kubeconfig)
		b := getClientBuilder(ctx).WithInterceptorFuncs(interceptor.Funcs{
			Watch: func(watchCtx context.Context, c client.WithWatch, obj client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
				if strings.Contains(kubeconfig, "slow-user") {
					close(slowReached)
					select {
					case <-slowRelease:
						return nil, errors.New("released")
					case <-watchCtx.Done():
						return nil, watchCtx.Err()
					}
				}
				return c.Watch(watchCtx, obj, opts...)
			},
		})
		return NewNeverCachingClient(b.Build()), nil
	}

	slowCluster := utiltestingapi.MakeMultiKueueCluster("cluster-slow").
		KubeConfig(kueue.SecretLocationType, "secret-slow").Generation(1).Obj()
	fastCluster := utiltestingapi.MakeMultiKueueCluster("cluster-fast").
		KubeConfig(kueue.SecretLocationType, "secret-fast").Generation(1).Obj()
	slowSecret := makeTestSecret("secret-slow", testKubeconfig("slow-user"))
	fastSecret := makeTestSecret("secret-fast", testKubeconfig("fast-user"))

	localClient := getClientBuilder(ctx).
		WithLists(&kueue.MultiKueueClusterList{Items: []kueue.MultiKueueCluster{*slowCluster, *fastCluster}}).
		WithLists(&corev1.SecretList{Items: []corev1.Secret{slowSecret, fastSecret}}).
		WithStatusSubresource(slowCluster, fastCluster).
		Build()

	recorder := &utiltesting.EventRecorder{}
	reconciler := newClustersReconciler(localClient, TestNamespace, 0, defaultOrigin, nil, nil, &NoOpClusterProfileAccessProvider{}, nil, recorder)
	reconciler.rootContext = ctx
	reconciler.builderOverride = gatedBuilder

	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster-slow"}})
	}()

	select {
	case <-slowReached:
	case <-time.After(stuckWatchTimeout):
		t.Fatal("slow cluster's reconcile did not reach the Watch call in time")
	}

	fastDone := make(chan error, 1)
	go func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster-fast"}})
		fastDone <- err
	}()

	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("cluster-fast reconcile returned err: %v", err)
		}
	case <-time.After(stuckWatchTimeout):
		t.Fatal("cluster-fast reconcile was blocked by cluster-slow (head-of-line)")
	}

	releaseSlow()
	select {
	case <-slowDone:
	case <-time.After(stuckWatchTimeout):
		t.Fatal("slow goroutine did not exit after release")
	}
}

func TestValidateKubeConfigPath(t *testing.T) {
	allowedDir := t.TempDir()
	// Create a real file under the allowed dir to test symlink resolution.
	validFile := filepath.Join(allowedDir, "worker.kubeconfig")
	if err := os.WriteFile(validFile, []byte("test"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create a symlink inside allowedDir that points outside.
	externalFile := filepath.Join(t.TempDir(), "external-secret")
	if err := os.WriteFile(externalFile, []byte("token"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	symlink := filepath.Join(allowedDir, "escape-symlink")
	if err := os.Symlink(externalFile, symlink); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cases := map[string]struct {
		path          string
		allowedPrefix string
		featureGate   bool
		wantErr       bool
		errContains   string
	}{
		"safe FG disabled allows any path (legacy)": {
			path:          "/any/arbitrary/path",
			allowedPrefix: allowedDir,
			featureGate:   false,
		},
		"valid path under prefix": {
			path:          validFile,
			allowedPrefix: allowedDir,
			featureGate:   true,
		},
		"empty path": {
			path:          "",
			allowedPrefix: allowedDir,
			wantErr:       true,
			errContains:   "must not be empty",
			featureGate:   true,
		},
		"path with dot-dot traversal": {
			path:          filepath.Join(allowedDir, "..", "etc", "passwd"),
			allowedPrefix: allowedDir,
			wantErr:       true,
			errContains:   "cannot resolve kubeconfig path symlinks",
			featureGate:   true,
		},
		"path outside prefix": {
			path:          "/var/run/secrets/kubernetes.io/serviceaccount/token",
			allowedPrefix: allowedDir,
			wantErr:       true,
			errContains:   "cannot resolve kubeconfig path symlinks",
			featureGate:   true,
		},
		"relative path rejected": {
			path:          "relative/path/file",
			allowedPrefix: allowedDir,
			wantErr:       true,
			errContains:   "must be absolute",
			featureGate:   true,
		},
		"symlink escape rejected": {
			path:          symlink,
			allowedPrefix: allowedDir,
			wantErr:       true,
			errContains:   "not under",
			featureGate:   true,
		},
		"SA token path rejected": {
			path:          "/var/run/secrets/kubernetes.io/serviceaccount/token",
			allowedPrefix: "/etc/multikueue/kubeconfigs",
			wantErr:       true,
			errContains:   "not under",
			featureGate:   true,
		},
		"prefix name collision": {
			// /etc/kueue-other should not match prefix /etc/kueue
			path:          "/etc/kueue-other/file",
			allowedPrefix: "/etc/kueue",
			wantErr:       true,
			errContains:   "not under",
			featureGate:   true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueKubeConfigPathValidation, tc.featureGate)
			_, err := validateKubeConfigPath(t.Context(), tc.path, tc.allowedPrefix)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errContains)
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errContains)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// hammerSetConfigWithReader runs reader concurrently with updateConfigAndRefreshWatchers
// swapping rc.client, as a regression harness for the #12557 data race. Only meaningful
// under `go test -race`.
func hammerSetConfigWithReader(t *testing.T, reader func(ctx context.Context, rc *remoteClient)) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	rc := newTestClient(ctx, []byte(testKubeconfig("worker1")), nil, nil)
	rc.origin = defaultOrigin
	rc.adapters = map[string]jobframework.MultiKueueAdapter{}

	// Seed an initial client so the first read has a client to observe.
	rc.connected.Store(false)
	if _, err := rc.updateConfigAndRefreshWatchers(ctx, rc.config); err != nil {
		t.Fatalf("seeding initial client: %v", err)
	}

	const iterations = 200
	var writerDone atomic.Bool
	var wg sync.WaitGroup

	// Writer: swap rc.client each iteration; fail if the update errors (swaps would stop).
	var writerErr error
	wg.Go(func() {
		defer writerDone.Store(true)
		for i := range iterations {
			cfg := &clientConfig{Kubeconfig: []byte(testKubeconfig(fmt.Sprintf("worker-%d", i)))}
			if _, err := rc.updateConfigAndRefreshWatchers(ctx, cfg); err != nil {
				writerErr = err
				return
			}
		}
	})

	wg.Go(func() {
		for !writerDone.Load() {
			reader(ctx, rc)
		}
	})

	wg.Wait()
	if writerErr != nil {
		t.Fatalf("writer updateConfigAndRefreshWatchers: %v", writerErr)
	}
}

func TestRemoteClientConcurrentSetConfigAndGC(t *testing.T) {
	hammerSetConfigWithReader(t, func(ctx context.Context, rc *remoteClient) {
		rc.runGC(ctx)
	})
}

func TestRemoteClientConcurrentSetConfigAndReaders(t *testing.T) {
	wlKey := client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: "wl"}
	hammerSetConfigWithReader(t, func(ctx context.Context, rc *remoteClient) {
		remoteCl := rc.getClient()
		if remoteCl == nil {
			return
		}
		_ = remoteCl.Get(ctx, wlKey, &kueue.Workload{}) // workload.go-style read
		_ = remoteCl.List(ctx, &kueue.LocalQueueList{}) // clusterqueue.go-style read
	})
}
