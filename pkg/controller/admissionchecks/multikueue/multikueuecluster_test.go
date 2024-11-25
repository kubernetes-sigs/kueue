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
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

var (
	errInvalidConfig = errors.New("invalid kubeconfig")
	errCannotWatch   = errors.New("client cannot watch")
)

func fakeClientBuilder(kubeconfig []byte, _ client.Options) (client.WithWatch, error) {
	if string(kubeconfig) == "invalid" {
		return nil, errInvalidConfig
	}
	b, _ := getClientBuilder()
	b = b.WithInterceptorFuncs(interceptor.Funcs{
		Watch: func(ctx context.Context, client client.WithWatch, obj client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
			if string(kubeconfig) == "nowatch" {
				return nil, errCannotWatch
			}
			return client.Watch(ctx, obj, opts...)
		},
	})
	return b.Build(), nil
}

func newTestClient(config string, watchCancel func()) *remoteClient {
	b, _ := getClientBuilder()
	localClient := b.Build()
	ret := &remoteClient{
		kubeconfig:  []byte(config),
		localClient: localClient,
		watchCancel: watchCancel,

		builderOverride: fakeClientBuilder,
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

func TestUpdateConfig(t *testing.T) {
	cancelCalledCount := 0
	cancelCalled := func() { cancelCalledCount++ }

	cases := map[string]struct {
		reconcileFor  string
		remoteClients map[string]*remoteClient
		clusters      []kueue.MultiKueueCluster
		secrets       []corev1.Secret

		wantRemoteClients map[string]*remoteClient
		wantClusters      []kueue.MultiKueueCluster
		wantRequeueAfter  time.Duration
		wantCancelCalled  int
	}{
		"new valid client is added": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "worker1 kubeconfig"),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with valid secret config": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "worker1 kubeconfig"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig", cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
			wantCancelCalled: 1,
		},
		"update client with valid path config": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, "testdata/worker1KubeConfig").
					Generation(1).
					Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig", cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, "testdata/worker1KubeConfig").
					Active(metav1.ConditionTrue, "Active", "Connected", 1).
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
			wantCancelCalled: 1,
		},
		"update client with invalid secret config": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "invalid"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig", cancelCalled),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient("invalid", nil),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
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
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, "").
					Generation(1).
					Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig", cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.PathLocationType, "").
					Active(metav1.ConditionFalse, "BadConfig", "open : no such file or directory", 1).
					Generation(1).
					Obj(),
			},
			wantCancelCalled: 1,
		},
		"missing cluster is removed": {
			reconcileFor: "worker2",
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 kubeconfig", cancelCalled),
				"worker2": newTestClient("worker2 kubeconfig", cancelCalled),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
			wantCancelCalled: 1,
		},
		"update client config, nowatch": {
			reconcileFor: "worker1",
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "nowatch"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig", cancelCalled),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient("nowatch", nil), 1),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
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
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "nowatch"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient("nowatch", cancelCalled), 2),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient("nowatch", nil), 3),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
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
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "good config"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient("nowatch", cancelCalled), 5),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient("good config", nil),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
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
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "client cannot watch", 1).
					Generation(1).
					Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "invalid"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": setReconnectState(newTestClient("nowatch", cancelCalled), 5),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": newTestClient("invalid", nil),
			},
			wantClusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, "worker1").
					Active(metav1.ConditionFalse, "ClientConnectionFailed", "invalid kubeconfig", 1).
					Generation(1).
					Obj(),
			},
			wantCancelCalled: 1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			builder = builder.WithLists(&kueue.MultiKueueClusterList{Items: tc.clusters})
			builder = builder.WithLists(&corev1.SecretList{Items: tc.secrets})
			builder = builder.WithStatusSubresource(slices.Map(tc.clusters, func(c *kueue.MultiKueueCluster) client.Object { return c })...)
			c := builder.Build()

			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New[string]("batch/job"))
			reconciler := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters)
			//nolint:fatcontext
			reconciler.rootContext = ctx

			if len(tc.remoteClients) > 0 {
				reconciler.remoteClients = tc.remoteClients
			}
			reconciler.builderOverride = fakeClientBuilder

			cancelCalledCount = 0
			res, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor}})
			if gotErr != nil {
				t.Errorf("unexpected reconcile error: %s", gotErr)
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
					return string(a.kubeconfig) == string(b.kubeconfig)
				})); diff != "" {
				t.Errorf("unexpected controllers (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestRemoteClientGC(t *testing.T) {
	baseJobBuilder := testingjob.MakeJob("job1", TestNamespace)
	baseWlBuilder := utiltesting.MakeWorkload("wl1", TestNamespace).ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "test-uuid")

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

	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manageBuilder, ctx := getClientBuilder()
			manageBuilder = manageBuilder.WithLists(&kueue.WorkloadList{Items: tc.managersWorkloads}, &batchv1.JobList{Items: tc.managersJobs})
			managerClient := manageBuilder.Build()

			worker1Builder, _ := getClientBuilder()
			worker1Builder = worker1Builder.WithLists(&kueue.WorkloadList{Items: tc.workersWorkloads}, &batchv1.JobList{Items: tc.workersJobs})
			worker1Client := worker1Builder.Build()

			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New[string]("batch/job"))
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
