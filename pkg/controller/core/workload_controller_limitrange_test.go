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

package core

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestLimitRangeSchedulingFieldsChanged(t *testing.T) {
	base := func() *corev1.LimitRange {
		return utiltesting.MakeLimitRange("limits", "ns").
			WithValue("Max", corev1.ResourceCPU, "2").
			WithValue("Min", corev1.ResourceCPU, "1").
			WithValue("Default", corev1.ResourceCPU, "2").
			WithValue("DefaultRequest", corev1.ResourceCPU, "1").
			Obj()
	}
	cases := map[string]struct {
		mutate func(*corev1.LimitRange)
		want   bool
	}{
		"no change": {
			mutate: func(*corev1.LimitRange) {},
			want:   false,
		},
		"max changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].Max[corev1.ResourceCPU] = resource.MustParse("8")
			},
			want: true,
		},
		"min changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].Min[corev1.ResourceCPU] = resource.MustParse("500m")
			},
			want: true,
		},
		"maxLimitRequestRatio changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].MaxLimitRequestRatio[corev1.ResourceCPU] = resource.MustParse("2")
			},
			want: true,
		},
		"default changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].Default[corev1.ResourceCPU] = resource.MustParse("3")
			},
			want: true,
		},
		"defaultRequest changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
			},
			want: true,
		},
		"item type changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].Type = corev1.LimitTypePod
			},
			want: true,
		},
		"item added": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits = append(lr.Spec.Limits, corev1.LimitRangeItem{Type: corev1.LimitTypePod})
			},
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			oldLr := base()
			newLr := base()
			tc.mutate(newLr)
			if got := limitRangeSchedulingFieldsChanged(oldLr, newLr); got != tc.want {
				t.Errorf("limitRangeSchedulingFieldsChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLimitRangeSchedulingFieldsChangedOnCreateOrDelete(t *testing.T) {
	withConstraints := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("Max", corev1.ResourceCPU, "2").Obj()
	defaultsOnly := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj()

	if !limitRangeSchedulingFieldsChanged(nil, withConstraints) {
		t.Error("limitRangeSchedulingFieldsChanged(nil, withConstraints) = false, want true")
	}
	if !limitRangeSchedulingFieldsChanged(defaultsOnly, nil) {
		t.Error("limitRangeSchedulingFieldsChanged(defaultsOnly, nil) = false, want true")
	}
	if limitRangeSchedulingFieldsChanged(nil, nil) {
		t.Error("limitRangeSchedulingFieldsChanged(nil, nil) = true, want false")
	}
}

func TestLimitRangeUpdateRetriesWorkloadPoppedWithStaleDefaults(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	oldLr := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("DefaultRequest", corev1.ResourceCPU, "3").Obj()
	ns := utiltesting.MakeNamespace("ns")
	wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue("lq").Obj()
	cl := utiltesting.NewClientBuilder().
		WithObjects(ns, oldLr).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
		WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
		Build()
	cache := schdcache.New(cl)
	queues, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, nil,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	if err := queues.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").Obj()); err != nil {
		t.Fatal(err)
	}
	if err := queues.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue("cq").Obj()); err != nil {
		t.Fatal(err)
	}
	if err := cl.Create(ctx, wl); err != nil {
		t.Fatal(err)
	}
	workload.AdjustResources(ctx, cl, wl)
	if err := queues.AddOrUpdateWorkload(log, wl); err != nil {
		t.Fatal(err)
	}

	heads := queues.Heads(ctx)
	if len(heads) != 1 {
		t.Fatalf("popped workloads = %d, want 1", len(heads))
	}
	if got := heads[0].TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 3000 {
		t.Fatalf("popped CPU = %d, want 3000", got)
	}

	newLr := oldLr.DeepCopy()
	newLr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("1")
	if err := cl.Update(ctx, newLr); err != nil {
		t.Fatal(err)
	}
	r := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{})
	h := &resourceUpdatesHandler{r: r}
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	h.Update(ctx, event.UpdateEvent{ObjectOld: oldLr, ObjectNew: newLr}, q)
	if moved := requeuer.ProcessRequeues(ctx); moved != 0 {
		t.Fatalf("workloads moved before scheduler requeue = %d, want 0", moved)
	}

	if !queues.RequeueWorkload(ctx, &heads[0].Info, qcache.RequeueReasonPreemptionNoCandidates, "") {
		t.Fatal("workload was not requeued")
	}
	headsCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	go queues.CleanUpOnContext(headsCtx)
	refreshedHeads := queues.Heads(headsCtx)
	if len(refreshedHeads) != 1 {
		t.Fatalf("active workloads after LimitRange update = %d, want 1", len(refreshedHeads))
	}
	if got := refreshedHeads[0].TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 1000 {
		t.Errorf("requeued CPU = %d, want 1000", got)
	}
	if got := client.ObjectKeyFromObject(refreshedHeads[0].Obj); got != client.ObjectKeyFromObject(wl) {
		t.Errorf("requeued workload = %v, want %v", got, client.ObjectKeyFromObject(wl))
	}
}
