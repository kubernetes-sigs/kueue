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

package leaderworkerset

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestEmptyPodGroupEvictionWithLiveLeaderWorkerSet(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.FinishOrphanedWorkloads, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	t.Cleanup(jobframework.EnableIntegrationsForTest(t, podcontroller.FrameworkName, FrameworkName))

	lws := &leaderworkersetv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "lws", Namespace: "ns", UID: "lws-uid"},
	}
	wl := utiltestingapi.MakeWorkload("test-group", "ns").Group().
		Finalizers(kueue.ResourceInUseFinalizerName).
		OwnerReference(leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet"), lws.Name, string(lws.UID)).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now()).
		AdmittedAt(true, time.Now()).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadEvicted,
			Status:  metav1.ConditionTrue,
			Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
			Message: "Exceeded the PodsReady timeout",
		}).
		Obj()
	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
		WithObjects(lws, wl).
		WithStatusSubresource(wl)
	indexer := utiltesting.AsIndexer(clientBuilder)
	if err := indexer.IndexField(ctx, &corev1.Pod{}, podcontroller.PodGroupNameCacheKey, podcontroller.IndexPodGroupName); err != nil {
		t.Fatalf("Could not add index for %s field name: %v", podcontroller.PodGroupNameCacheKey, err)
	}
	cl := clientBuilder.Build()
	podReconciler, err := podcontroller.NewReconciler(ctx, cl, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("NewReconciler() error: %v", err)
	}
	if _, err := podReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "group/ns", Name: wl.Name},
	}); err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	got := &kueue.Workload{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), got); err != nil {
		t.Fatalf("Get Workload: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, kueue.ResourceInUseFinalizerName) {
		t.Error("Workload finalizer was removed while its LeaderWorkerSet owner is live")
	}
	if workload.HasQuotaReservation(got) {
		t.Error("Workload quota reservation was not cleared after eviction")
	}
}
