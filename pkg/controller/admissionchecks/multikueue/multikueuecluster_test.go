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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
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
	ret.watchCancel = map[string]func(){
		"test": func() {
			ret.kubeconfig = []byte(string(ret.kubeconfig) + " canceled")
		},
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
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "worker1 kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Active(metav1.ConditionTrue, "Active", "Connected").Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with valid secret config": {
			reconcileFor: "worker1",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "worker1 kubeconfig"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Active(metav1.ConditionTrue, "Active", "Connected").Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with valid path config": {
			reconcileFor: "worker1",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.PathLocationType, "testdata/worker1KubeConfig").Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.PathLocationType, "testdata/worker1KubeConfig").Active(metav1.ConditionTrue, "Active", "Connected").Obj(),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with invalid secret config": {
			reconcileFor: "worker1",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Obj(),
			},
			secrets: []corev1.Secret{
				makeTestSecret("worker1", "invalid"),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Active(metav1.ConditionFalse, "ClientConnectionFailed", "invalid kubeconfig").Obj(),
			},
		},
		"update client with invalid path config": {
			reconcileFor: "worker1",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.PathLocationType, "").Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.PathLocationType, "").Active(metav1.ConditionFalse, "BadConfig", "open : no such file or directory").Obj(),
			},
		},
		"missing cluster is removed": {
			reconcileFor: "worker2",
			clusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Obj(),
			},
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 kubeconfig"),
				"worker2": newTestClient("worker2 kubeconfig"),
			},
			wantClusters: []kueuealpha.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "worker1").Obj(),
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

			reconciler := newClustersReconciler(c, TestNamespace, 0, defaultOrigin)
			reconciler.rootContext = ctx

			if len(tc.remoteClients) > 0 {
				reconciler.remoteClients = tc.remoteClients
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

			if diff := cmp.Diff(tc.wantRemoteClients, reconciler.remoteClients, cmpopts.EquateEmpty(),
				cmp.Comparer(func(a, b remoteClient) bool { return string(a.kubeconfig) == string(b.kubeconfig) })); diff != "" {
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
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
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
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
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
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
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
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"missing worker workloads and their owner jobs are deleted": {
			workersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
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
					Label(kueuealpha.MultiKueueOriginLabel, "other-gc-key").
					Obj(),
			},
			workersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Obj(),
			},
			wantWorkersWorkloads: []kueue.Workload{
				*baseWlBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, "other-gc-key").
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

			w1remoteClient := newRemoteClient(managerClient, nil, defaultOrigin)
			w1remoteClient.client = worker1Client

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
