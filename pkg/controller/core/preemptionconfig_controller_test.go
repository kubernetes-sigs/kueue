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
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPreemptionConfigReconciler_Reconcile(t *testing.T) {
	// Every ClusterQueue has one inadmissible Workload named "wl-<ClusterQueue>".
	cqConfigs := map[kueue.ClusterQueueReference]string{
		"cq-a":    "config-a",
		"cq-b":    "config-b",
		"cq-none": "",
	}
	testCases := map[string]struct {
		disableGate bool
		// configs are the PreemptionConfigs that exist in the API server.
		configs      []string
		config       string
		wantRequeued []kueue.ClusterQueueReference
	}{
		"PreemptionConfig created or updated requeues only the ClusterQueues referencing it": {
			configs:      []string{"config-a", "config-b"},
			config:       "config-a",
			wantRequeued: []kueue.ClusterQueueReference{"cq-a"},
		},
		"PreemptionConfig deleted requeues the ClusterQueues still referencing it": {
			configs:      []string{"config-b"},
			config:       "config-a",
			wantRequeued: []kueue.ClusterQueueReference{"cq-a"},
		},
		"PreemptionConfig not referenced by any ClusterQueue requeues nothing": {
			configs: []string{"config-a", "config-b", "unused-config"},
			config:  "unused-config",
		},
		"ConfigurablePreemptions disabled requeues nothing": {
			disableGate: true,
			configs:     []string{"config-a", "config-b"},
			config:      "config-a",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, !tc.disableGate)
			ctx, log := utiltesting.ContextWithLog(t)
			flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
			objs := []client.Object{utiltesting.MakeNamespace("ns"), flavor}
			for _, config := range tc.configs {
				objs = append(objs, utiltestingalpha.MakePreemptionConfig(config).Obj())
			}
			var cqs []*kueue.ClusterQueue
			var lqs []*kueue.LocalQueue
			wantInadmissible := make(map[kueue.ClusterQueueReference][]workload.Reference)
			for cqName, config := range cqConfigs {
				cq := utiltestingapi.MakeClusterQueue(string(cqName)).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj())
				if config != "" {
					cq.Annotation(kueuealpha.PreemptionConfigNameAnnotation, config)
				}
				cqs = append(cqs, cq.Obj())
				lq := utiltestingapi.MakeLocalQueue("lq-"+string(cqName), "ns").ClusterQueue(string(cqName)).Obj()
				lqs = append(lqs, lq)
				wlName := "wl-" + string(cqName)
				objs = append(objs, lq,
					utiltestingapi.MakeWorkload(wlName, "ns").Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "1").Obj())
				if !slices.Contains(tc.wantRequeued, cqName) {
					wantInadmissible[cqName] = []workload.Reference{workload.NewReference("ns", wlName)}
				}
			}
			cl := utiltesting.NewClientBuilder().WithObjects(objs...).Build()
			cache := schdcache.New(cl)
			cache.AddOrUpdateResourceFlavor(log, flavor)
			queues, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			for _, cq := range cqs {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
				if err := queues.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
			}
			for _, lq := range lqs {
				if err := queues.AddLocalQueue(ctx, lq); err != nil {
					t.Fatal(err)
				}
			}
			// Setup notifies the ClusterQueues too; drain those so only Reconcile's notifications count.
			requeuer.ProcessRequeues(ctx)
			for _, head := range queues.Heads(ctx) {
				queues.RequeueWorkload(ctx, &head.Info, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota)
			}

			r := NewPreemptionConfigReconciler(queues, cache, nil)
			if _, err := r.Reconcile(ctx, reconcile.Request{Name: tc.config}); err != nil {
				t.Fatal(err)
			}
			requeuer.ProcessRequeues(ctx)

			if diff := cmp.Diff(wantInadmissible, queues.DumpInadmissible()); diff != "" {
				t.Errorf("Unexpected inadmissible workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionConfigReconciler_Predicates(t *testing.T) {
	config := func(generation int64) *utiltestingalpha.PreemptionConfigWrapper {
		pc := utiltestingalpha.MakePreemptionConfig("config")
		pc.Generation = generation
		return pc
	}
	testCases := map[string]struct {
		event any
		want  bool
	}{
		"create": {
			event: event.TypedCreateEvent[*kueuealpha.PreemptionConfig]{Object: config(1).Obj()},
			want:  true,
		},
		"update with a spec change": {
			event: event.TypedUpdateEvent[*kueuealpha.PreemptionConfig]{
				ObjectOld: config(1).Obj(),
				ObjectNew: config(2).Obj(),
			},
			want: true,
		},
		"update without a spec change": {
			event: event.TypedUpdateEvent[*kueuealpha.PreemptionConfig]{
				ObjectOld: config(1).Obj(),
				ObjectNew: func() *kueuealpha.PreemptionConfig {
					pc := config(1).Obj()
					pc.Labels = map[string]string{"team": "ml"}
					return pc
				}(),
			},
			want: false,
		},
		"delete": {
			event: event.TypedDeleteEvent[*kueuealpha.PreemptionConfig]{Object: config(1).Obj()},
			want:  true,
		},
		"generic": {
			event: event.TypedGenericEvent[*kueuealpha.PreemptionConfig]{Object: config(1).Obj()},
			want:  false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			r := &PreemptionConfigReconciler{}
			var got bool
			switch e := tc.event.(type) {
			case event.TypedCreateEvent[*kueuealpha.PreemptionConfig]:
				got = r.Create(e)
			case event.TypedUpdateEvent[*kueuealpha.PreemptionConfig]:
				got = r.Update(e)
			case event.TypedDeleteEvent[*kueuealpha.PreemptionConfig]:
				got = r.Delete(e)
			case event.TypedGenericEvent[*kueuealpha.PreemptionConfig]:
				got = r.Generic(e)
			default:
				t.Fatalf("unexpected event type %T", tc.event)
			}
			if got != tc.want {
				t.Errorf("Unexpected predicate result: want %v, got %v", tc.want, got)
			}
		})
	}
}
