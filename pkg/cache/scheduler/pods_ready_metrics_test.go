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

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPodsReadyActiveWorkloads(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	role := roletracker.RoleStandalone
	sortDataPoints := cmpopts.SortSlices(func(a, b testingmetrics.MetricDataPoint) bool {
		return a.Less(&b)
	})

	baseWl := utiltestingapi.MakeWorkload("", metav1.NamespaceDefault).
		Queue("lq").
		Request(corev1.ResourceCPU, "1").
		SimpleReserveQuota("cq", "default", now)

	cqBaseLabels := map[string]string{"cluster_queue": "cq", "replica_role": role}
	lqBaseLabels := map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role}

	wl1Reserved := baseWl.Clone().Name("wl1").UID("wl1-uid").PodsReadyAt(true, now).Obj()
	wl1NotReady := baseWl.Clone().Name("wl1").UID("wl1-uid").AdmittedAt(true, now).PodsReadyAt(false, now).Obj()
	wl1Ready := baseWl.Clone().Name("wl1").UID("wl1-uid").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wl2Ready := baseWl.Clone().Name("wl2").UID("wl2-uid").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wl1Finished := baseWl.Clone().Name("wl1").UID("wl1-uid").AdmittedAt(true, now).PodsReadyAt(true, now).FinishedAt(now).Obj()
	wl2Deactivated := baseWl.Clone().Name("wl2").UID("wl2-uid").AdmittedAt(true, now).PodsReadyAt(true, now).Active(false).Obj()
	wl3Ready := baseWl.Clone().Name("wl3").UID("wl3-uid").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wl4Ready := baseWl.Clone().Name("wl4").UID("wl4-uid").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wl4Tombstone := utiltestingapi.MakeWorkload("wl4", metav1.NamespaceDefault).UID(types.UID("wl4-uid")).Obj()

	lqExposed := utiltestingapi.MakeLocalQueue("lq-exposed", metav1.NamespaceDefault).
		ClusterQueue("cq").Label("expose", "true").Obj()
	lqHidden := utiltestingapi.MakeLocalQueue("lq-hidden", metav1.NamespaceDefault).
		ClusterQueue("cq").Label("expose", "false").Obj()
	lqNowExposed := lqHidden.DeepCopy()
	lqNowExposed.Labels["expose"] = "true"
	wlExposed := baseWl.Clone().Name("wl-exposed").UID("wl-exposed-uid").Queue("lq-exposed").
		Label("kind", "training").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wlHidden := baseWl.Clone().Name("wl-hidden").UID("wl-hidden-uid").Queue("lq-hidden").
		Label("kind", "inference").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wlHiddenFinished := baseWl.Clone().Name("wl-hidden").UID("wl-hidden-uid").Queue("lq-hidden").
		Label("kind", "inference").AdmittedAt(true, now).PodsReadyAt(true, now).FinishedAt(now).Obj()
	wlHiddenTraining := baseWl.Clone().Name("wl-hidden-training").UID("wl-hidden-training-uid").Queue("lq-hidden").
		Label("kind", "training").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()

	wlLabeled1 := baseWl.Clone().Name("wl1").UID("wl1-uid").
		Label("kind", "training").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wlLabeled1Relabeled := wlLabeled1.DeepCopy()
	wlLabeled1Relabeled.Labels["kind"] = "inference"
	wlUntracked := baseWl.Clone().Name("wl-untracked").UID("wl-untracked-uid").
		Label("kind", "batch").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	lqTeamData := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).
		Label("team", "data").ClusterQueue("cq").Obj()

	parentWl := baseWl.Clone().Name("parent").UID("parent-uid").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	variantWl := baseWl.Clone().Name("variant").UID("variant-uid").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	variantWl.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: kueue.SchemeGroupVersion.String(),
		Kind:       "Workload",
		Name:       "parent",
		UID:        "parent-uid",
	}}

	type podsReadyStep struct {
		name          string
		addOrUpdateWl *kueue.Workload
		wantAddResult *bool
		deleteWlKey   workload.Reference
		forgetWl      *kueue.Workload
		addLQs        []*kueue.LocalQueue
		updateLQOld   *kueue.LocalQueue
		updateLQNew   *kueue.LocalQueue
		resyncLQKey   queue.LocalQueueReference
		wantCQ        []testingmetrics.MetricDataPoint
		wantLQ        []testingmetrics.MetricDataPoint
	}

	cases := map[string]struct {
		featureGates        map[featuregate.Feature]bool
		customLabelSpecs    []configapi.ControllerMetricsCustomLabel
		lqSelector          *metav1.LabelSelector
		cqLabels            map[string]string
		concurrentAdmission bool
		addDefaultLQ        bool
		steps               []podsReadyStep
	}{
		"CustomMetricLabels disabled lifecycle, flapping, deactivation, finish, eviction, and ForgetWorkload": {
			featureGates: map[featuregate.Feature]bool{
				features.CustomMetricLabels: false,
			},
			addDefaultLQ: true,
			steps: []podsReadyStep{
				{
					name:          "quota-reserved workload not yet admitted with stale PodsReady=True emits no series",
					addOrUpdateWl: wl1Reserved,
				},
				{
					name:          "admitted with PodsReady=False materializes 0",
					addOrUpdateWl: wl1NotReady,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 0}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 0}},
				},
				{
					name:          "PodsReady transitions False -> True",
					addOrUpdateWl: wl1Ready,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 1}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 1}},
				},
				{
					name:          "PodsReady flaps True -> False",
					addOrUpdateWl: wl1NotReady,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 0}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 0}},
				},
				{
					name:          "PodsReady flaps False -> True",
					addOrUpdateWl: wl1Ready,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 1}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 1}},
				},
				{
					name:          "second admitted PodsReady=True workload increments to 2",
					addOrUpdateWl: wl2Ready,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 2}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 2}},
				},
				{
					name:          "finishing wl1 while PodsReady=True decrements to 1",
					addOrUpdateWl: wl1Finished,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 1}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 1}},
				},
				{
					name:          "deactivating wl2 while PodsReady=True decrements to 0",
					addOrUpdateWl: wl2Deactivated,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 0}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 0}},
				},
				{
					name:          "adding wl3 PodsReady=True increments to 1",
					addOrUpdateWl: wl3Ready,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 1}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 1}},
				},
				{
					name:        "evicting wl3 via DeleteWorkload decrements to 0",
					deleteWlKey: workload.Key(wl3Ready),
					wantCQ:      []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 0}},
					wantLQ:      []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 0}},
				},
				{
					name:          "adding wl4 PodsReady=True increments to 1",
					addOrUpdateWl: wl4Ready,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 1}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 1}},
				},
				{
					name:     "ForgetWorkload with conditionless tombstone decrements to 0",
					forgetWl: wl4Tombstone,
					wantCQ:   []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 0}},
					wantLQ:   []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 0}},
				},
			},
		},
		"LocalQueueMetrics disabled reports only ClusterQueue gauge": {
			featureGates: map[featuregate.Feature]bool{
				features.CustomMetricLabels: false,
				features.LocalQueueMetrics:  false,
			},
			addDefaultLQ: true,
			steps: []podsReadyStep{
				{
					name:          "admitted PodsReady=True reports only CQ series",
					addOrUpdateWl: wl1Ready,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 1}},
				},
				{
					name:          "transition to PodsReady=False updates only CQ series",
					addOrUpdateWl: wl1NotReady,
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 0}},
				},
			},
		},
		"LocalQueueMetrics selector, late AddLocalQueue, and selector opt-in zero pruning": {
			customLabelSpecs: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").
					SourceKind(configapi.SourceKindWorkload).TrackedValues("training", "inference").Obj(),
			},
			lqSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"expose": "true"},
			},
			steps: []podsReadyStep{
				{
					name:          "running workload on exposed LQ before AddLocalQueue",
					addOrUpdateWl: wlExposed,
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
					},
				},
				{
					name:          "running workload on hidden LQ before AddLocalQueue",
					addOrUpdateWl: wlHidden,
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "inference"}, Value: 1},
					},
				},
				{
					name:   "late AddLocalQueue exposes only matching LocalQueue",
					addLQs: []*kueue.LocalQueue{lqExposed, lqHidden},
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "inference"}, Value: 1},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq-exposed", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
					},
				},
				{
					name:          "finishing wl-hidden while lq-hidden is excluded",
					addOrUpdateWl: wlHiddenFinished,
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "inference"}, Value: 0},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq-exposed", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
					},
				},
				{
					name:        "opting lq-hidden into QueueSelector and resyncing does not emit finished inference series",
					updateLQOld: lqHidden,
					updateLQNew: lqNowExposed,
					resyncLQKey: queue.Key(lqNowExposed),
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "inference"}, Value: 0},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq-exposed", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
					},
				},
				{
					name:          "adding training workload to lq-hidden does not resurrect inference label set",
					addOrUpdateWl: wlHiddenTraining,
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "training"}, Value: 2},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_wl_kind": "inference"}, Value: 0},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq-exposed", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
						{Labels: map[string]string{"name": "lq-hidden", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_wl_kind": "training"}, Value: 1},
					},
				},
			},
		},
		"CustomMetricLabels with ClusterQueue, LocalQueue, and Workload labels including relabel and late AddLocalQueue": {
			customLabelSpecs: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("cq_team").SourceLabelKey("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
				utiltestingapi.MakeCustomLabel("lq_team").SourceLabelKey("team").SourceKind(configapi.SourceKindLocalQueue).Obj(),
				utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").SourceKind(configapi.SourceKindWorkload).TrackedValues("training", "inference").Obj(),
			},
			cqLabels: map[string]string{"team": "ml"},
			steps: []podsReadyStep{
				{
					name:          "tracked workload added before AddLocalQueue",
					addOrUpdateWl: wlLabeled1,
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "training"}, Value: 1},
					},
				},
				{
					name:          "untracked workload added before AddLocalQueue",
					addOrUpdateWl: wlUntracked,
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "training"}, Value: 1},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": configapi.UntrackedCustomLabelValue}, Value: 1},
					},
				},
				{
					name:   "late AddLocalQueue populates existing workloads on LQ series",
					addLQs: []*kueue.LocalQueue{lqTeamData},
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "training"}, Value: 1},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": configapi.UntrackedCustomLabelValue}, Value: 1},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_lq_team": "data", "custom_wl_kind": "training"}, Value: 1},
						{
							Labels: map[string]string{
								"name":           "lq",
								"namespace":      metav1.NamespaceDefault,
								"replica_role":   role,
								"custom_lq_team": "data",
								"custom_wl_kind": configapi.UntrackedCustomLabelValue,
							},
							Value: 1,
						},
					},
				},
				{
					name:          "relabeling wl1 from training to inference moves gauge count",
					addOrUpdateWl: wlLabeled1Relabeled,
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "training"}, Value: 0},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "inference"}, Value: 1},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": configapi.UntrackedCustomLabelValue}, Value: 1},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_lq_team": "data", "custom_wl_kind": "training"}, Value: 0},
						{Labels: map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_lq_team": "data", "custom_wl_kind": "inference"}, Value: 1},
						{
							Labels: map[string]string{
								"name":           "lq",
								"namespace":      metav1.NamespaceDefault,
								"replica_role":   role,
								"custom_lq_team": "data",
								"custom_wl_kind": configapi.UntrackedCustomLabelValue,
							},
							Value: 1,
						},
					},
				},
				{
					name:        "deleting relabeled wl1 drops inference series to 0",
					deleteWlKey: workload.Key(wlLabeled1Relabeled),
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "training"}, Value: 0},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "inference"}, Value: 0},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": configapi.UntrackedCustomLabelValue}, Value: 1},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_lq_team": "data", "custom_wl_kind": "training"}, Value: 0},
						{Labels: map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_lq_team": "data", "custom_wl_kind": "inference"}, Value: 0},
						{
							Labels: map[string]string{
								"name":           "lq",
								"namespace":      metav1.NamespaceDefault,
								"replica_role":   role,
								"custom_lq_team": "data",
								"custom_wl_kind": configapi.UntrackedCustomLabelValue,
							},
							Value: 1,
						},
					},
				},
				{
					name:        "deleting untracked workload drops all series to 0",
					deleteWlKey: workload.Key(wlUntracked),
					wantCQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "training"}, Value: 0},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": "inference"}, Value: 0},
						{Labels: map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_cq_team": "ml", "custom_wl_kind": configapi.UntrackedCustomLabelValue}, Value: 0},
					},
					wantLQ: []testingmetrics.MetricDataPoint{
						{Labels: map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_lq_team": "data", "custom_wl_kind": "training"}, Value: 0},
						{Labels: map[string]string{"name": "lq", "namespace": metav1.NamespaceDefault, "replica_role": role, "custom_lq_team": "data", "custom_wl_kind": "inference"}, Value: 0},
						{
							Labels: map[string]string{
								"name":           "lq",
								"namespace":      metav1.NamespaceDefault,
								"replica_role":   role,
								"custom_lq_team": "data",
								"custom_wl_kind": configapi.UntrackedCustomLabelValue,
							},
							Value: 0,
						},
					},
				},
			},
		},
		"ConcurrentAdmission ignores parent workload and counts variant workload": {
			featureGates: map[featuregate.Feature]bool{
				features.CustomMetricLabels:  false,
				features.ConcurrentAdmission: true,
			},
			concurrentAdmission: true,
			addDefaultLQ:        true,
			steps: []podsReadyStep{
				{
					name:          "non-variant parent workload is skipped",
					addOrUpdateWl: parentWl,
					wantAddResult: new(false),
				},
				{
					name:          "variant workload is counted",
					addOrUpdateWl: variantWl,
					wantAddResult: new(true),
					wantCQ:        []testingmetrics.MetricDataPoint{{Labels: cqBaseLabels, Value: 1}},
					wantLQ:        []testingmetrics.MetricDataPoint{{Labels: lqBaseLabels, Value: 1}},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			if len(tc.featureGates) > 0 {
				features.SetFeatureGatesDuringTest(t, tc.featureGates)
			}

			selector := labels.Everything()
			if tc.lqSelector != nil {
				var err error
				selector, err = metav1.LabelSelectorAsSelector(tc.lqSelector)
				if err != nil {
					t.Fatalf("Invalid selector: %v", err)
				}
			}

			opts := []Option{
				WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
					Enabled:       true,
					QueueSelector: selector,
				}),
			}
			var customLabels *metrics.CustomLabels
			if len(tc.customLabelSpecs) > 0 {
				customLabels = metrics.NewCustomLabels(tc.customLabelSpecs)
				opts = append(opts, WithCustomLabels(customLabels))
			} else {
				metrics.InitMetricVectors(nil)
			}

			cache := New(utiltesting.NewFakeClient(), opts...)
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

			cqBuilder := utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj())
			for k, v := range tc.cqLabels {
				cqBuilder = cqBuilder.Label(k, v)
			}
			cq := cqBuilder.Obj()
			if tc.concurrentAdmission {
				cq.Spec.ConcurrentAdmissionPolicy = &kueue.ConcurrentAdmissionPolicy{}
			}
			if customLabels != nil {
				customLabels.CQStore("cq", cq.Labels, cq.Annotations)
			}
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed to add ClusterQueue: %v", err)
			}
			if tc.addDefaultLQ {
				lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).ClusterQueue("cq").Obj()
				if err := cache.AddLocalQueue(lq); err != nil {
					t.Fatalf("Failed to add LocalQueue: %v", err)
				}
			}

			for i, step := range tc.steps {
				switch {
				case step.addOrUpdateWl != nil:
					added := cache.AddOrUpdateWorkload(ctx, log, step.addOrUpdateWl.DeepCopy())
					if step.wantAddResult != nil && added != *step.wantAddResult {
						t.Fatalf("Step %d (%s): AddOrUpdateWorkload = %v, want %v", i, step.name, added, *step.wantAddResult)
					}
				case step.deleteWlKey != "":
					if err := cache.DeleteWorkload(log, step.deleteWlKey); err != nil {
						t.Fatalf("Step %d (%s): DeleteWorkload failed: %v", i, step.name, err)
					}
				case step.forgetWl != nil:
					if _, err := cache.ForgetWorkload(log, step.forgetWl.DeepCopy()); err != nil {
						t.Fatalf("Step %d (%s): ForgetWorkload failed: %v", i, step.name, err)
					}
				case len(step.addLQs) > 0:
					for _, lq := range step.addLQs {
						if err := cache.AddLocalQueue(lq.DeepCopy()); err != nil {
							t.Fatalf("Step %d (%s): AddLocalQueue(%s) failed: %v", i, step.name, lq.Name, err)
						}
					}
				case step.updateLQOld != nil && step.updateLQNew != nil:
					if err := cache.UpdateLocalQueue(step.updateLQOld.DeepCopy(), step.updateLQNew.DeepCopy()); err != nil {
						t.Fatalf("Step %d (%s): UpdateLocalQueue failed: %v", i, step.name, err)
					}
					if step.resyncLQKey != "" {
						cache.ResyncLocalQueueGaugeMetrics("cq", step.resyncLQKey)
					}
				}

				gotCQ := testingmetrics.CollectFilteredGaugeVec(metrics.PodsReadyActiveWorkloads, map[string]string{"cluster_queue": "cq"})
				if diff := cmp.Diff(step.wantCQ, gotCQ, sortDataPoints, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Step %d (%s): unexpected CQ pods_ready_active_workloads (-want,+got):\n%s", i, step.name, diff)
				}
				gotLQ := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{"namespace": metav1.NamespaceDefault})
				if diff := cmp.Diff(step.wantLQ, gotLQ, sortDataPoints, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Step %d (%s): unexpected LQ pods_ready_active_workloads (-want,+got):\n%s", i, step.name, diff)
				}
			}
		})
	}
}
