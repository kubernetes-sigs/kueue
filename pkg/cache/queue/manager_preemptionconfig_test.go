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

package queue

import (
	"context"
	"maps"
	"testing"
	"time"

	gocmp "github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	kueuetestalpha1 "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// A workload found inadmissible under one set of preemption rules may be
// admissible under another, so starting to reference a PreemptionConfig,
// switching to another one or dropping the reference retries the workloads.
func TestUpdateClusterQueuePreemptionConfig(t *testing.T) {
	both := map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/head", "default/queued"}}
	cases := map[string]struct {
		configurablePreemptions bool
		annotations             map[string]string
		updatedAnnotations      map[string]string
		failFirstUpdate         bool
		wantActive              map[kueue.ClusterQueueReference][]workload.Reference
		wantInadmissible        map[kueue.ClusterQueueReference][]workload.Reference
	}{
		"referencing a PreemptionConfig retries the bulk-moved workloads": {
			configurablePreemptions: true,
			updatedAnnotations:      map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "config"},
			wantActive:              both,
		},
		"referencing a PreemptionConfig after a failed update retries the bulk-moved workloads": {
			configurablePreemptions: true,
			updatedAnnotations:      map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "config"},
			failFirstUpdate:         true,
			wantActive:              both,
		},
		"an unrelated annotation does not retry the workloads": {
			configurablePreemptions: true,
			updatedAnnotations:      map[string]string{"example.com/unrelated": "true"},
			wantInadmissible:        both,
		},
		"the PreemptionConfig is ignored with ConfigurablePreemptions disabled": {
			updatedAnnotations: map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "config"},
			wantInadmissible:   both,
		},
		"referencing another PreemptionConfig retries the workloads": {
			configurablePreemptions: true,
			annotations:             map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "restrictive"},
			updatedAnnotations:      map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "permissive"},
			wantActive:              both,
		},
		"dropping the PreemptionConfig retries the workloads": {
			configurablePreemptions: true,
			annotations:             map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "restrictive"},
			wantActive:              both,
		},
		"keeping the PreemptionConfig does not retry the workloads": {
			configurablePreemptions: true,
			annotations:             map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "restrictive"},
			updatedAnnotations:      map[string]string{kueuealpha.PreemptionConfigNameAnnotation: "restrictive", "example.com/unrelated": "true"},
			wantActive:              map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/queued"}},
			wantInadmissible:        map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/head"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, tc.configurablePreemptions)
			ctx, log := utiltesting.ContextWithLog(t)
			cq := utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.BestEffortFIFO).Obj()
			cq.Annotations = tc.annotations
			lq := utiltestingapi.MakeLocalQueue("lq", defaultNamespace).ClusterQueue("cq").Obj()
			now := time.Now()
			head := utiltestingapi.MakeWorkload("head", defaultNamespace).Queue("lq").Creation(now).
				Request(corev1.ResourceCPU, "1").Obj()
			queued := utiltestingapi.MakeWorkload("queued", defaultNamespace).Queue("lq").Creation(now.Add(time.Second)).
				Request(corev1.ResourceCPU, "1").Obj()
			cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace))
			manager, watcher := NewManagerForUnitTestsWithRequeuer(cl, nil)
			if err := manager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed adding ClusterQueue: %v", err)
			}
			if err := manager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("Failed adding LocalQueue: %v", err)
			}
			// Drain the requeues of the setup, so only the update can trigger one.
			watcher.ProcessRequeues(ctx)
			// Increase the popCycle to ensure that the head will be added as inadmissible.
			manager.getClusterQueue("cq").popCycle++
			for _, wl := range []*kueue.Workload{head, queued} {
				if err := cl.Create(ctx, wl); err != nil {
					t.Fatalf("Failed adding Workload to the client: %v", err)
				}
			}
			manager.getClusterQueue("cq").PushOrUpdate(workload.NewInfo(log, queued))
			manager.RequeueWorkload(ctx, workload.NewInfo(log, head), RequeueReasonPreemptionNoCandidates, kueue.WorkloadQuotaReservedReasonWaitingForQuota)
			// Only the head is inadmissible when the PreemptionConfig, missing here, prevents the bulk move.
			wantBefore := both
			if _, ok := tc.annotations[kueuealpha.PreemptionConfigNameAnnotation]; ok && tc.configurablePreemptions {
				wantBefore = map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/head"}}
			}
			if diff := gocmp.Diff(wantBefore, manager.DumpInadmissible(), cmpDump); diff != "" {
				t.Fatalf("Unexpected inadmissible workloads before the update (-want,+got):\n%s", diff)
			}

			updated := cq.DeepCopy()
			updated.Annotations = tc.updatedAnnotations
			if tc.failFirstUpdate {
				invalid := updated.DeepCopy()
				invalid.Spec.NamespaceSelector = &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "key", Operator: "Invalid"}},
				}
				if err := manager.UpdateClusterQueue(log, invalid, false); err == nil {
					t.Fatal("Expected the update with an invalid namespaceSelector to fail")
				}
				watcher.ProcessRequeues(ctx)
			}
			if err := manager.UpdateClusterQueue(log, updated, false); err != nil {
				t.Fatalf("Failed to update ClusterQueue: %v", err)
			}
			watcher.ProcessRequeues(ctx)

			if diff := gocmp.Diff(tc.wantActive, manager.Dump(), cmpDump); diff != "" {
				t.Errorf("Unexpected active workloads (-want,+got):\n%s", diff)
			}
			if diff := gocmp.Diff(tc.wantInadmissible, manager.DumpInadmissible(), cmpDump); diff != "" {
				t.Errorf("Unexpected inadmissible workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

// A requeue reads the PreemptionConfig, so that the requeued workload takes to
// inadmissible only the workloads its rules cannot tell apart from it, also the
// ones pushed or relabeled after the bulk move.
func TestRequeueWorkloadReadsPreemptionConfig(t *testing.T) {
	byTeam := kueuetestalpha1.MakePreemptionConfig("config").
		Rules(kueuetestalpha1.MakePreemptionRule("rule", kueuealpha.Always,
			kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).PreemptorSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}).Obj()).
		Obj()
	wholeClass := map[kueue.ClusterQueueReference][]workload.Reference{
		"cq": {"default/head", "default/team-a", "default/team-a-later", "default/team-b", "default/team-b-later"},
	}
	cases := map[string]struct {
		disableConfigurablePreemptions bool
		config                         *kueuealpha.PreemptionConfig
		wantActive                     map[kueue.ClusterQueueReference][]workload.Reference
		wantInadmissible               map[kueue.ClusterQueueReference][]workload.Reference
		wantNoFitReason                string
	}{
		"workloads differing in a label read by a rule are not bulk-moved": {
			config:           byTeam,
			wantActive:       map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/team-a", "default/team-a-later", "default/team-b"}},
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/head", "default/team-b-later"}},
			wantNoFitReason:  kueue.WorkloadQuotaReservedReasonWaitingForQuota,
		},
		"rules reading nothing of the preemptor bulk-move the whole equivalence class": {
			config: kueuetestalpha1.MakePreemptionConfig("config").
				Rule("rule", kueuealpha.Always, kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj()).
				Obj(),
			wantInadmissible: wholeClass,
			wantNoFitReason:  kueue.WorkloadQuotaReservedReasonWaitingForQuota,
		},
		"a PreemptionConfig that cannot be read prevents the bulk move": {
			wantActive:       map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/team-a", "default/team-a-later", "default/team-b", "default/team-b-later"}},
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/head"}},
		},
		"the PreemptionConfig is ignored with ConfigurablePreemptions disabled": {
			disableConfigurablePreemptions: true,
			config:                         byTeam,
			wantInadmissible:               wholeClass,
			wantNoFitReason:                kueue.WorkloadQuotaReservedReasonWaitingForQuota,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, !tc.disableConfigurablePreemptions)
			ctx, log := utiltesting.ContextWithLog(t)
			cq := utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.BestEffortFIFO).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, "config").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", defaultNamespace).ClusterQueue("cq").Obj()
			// The job UID label, which no rule reads, must not split the equivalence class.
			newInfo := func(name, team string) *workload.Info {
				return workload.NewInfo(log, utiltestingapi.MakeWorkload(name, defaultNamespace).Queue("lq").
					Label("team", team).Label("kueue.x-k8s.io/job-uid", name).
					Request(corev1.ResourceCPU, "1").Obj())
			}
			head := newInfo("head", "b")
			// Set by Heads when the scheduler pops the workload.
			head.ClusterQueue = "cq"
			objs := []client.Object{utiltesting.MakeNamespace(defaultNamespace)}
			if tc.config != nil {
				objs = append(objs, tc.config)
			}
			cl := utiltesting.NewFakeClient(objs...)
			manager, watcher := NewManagerForUnitTestsWithRequeuer(cl, nil, WithPreemptionExpectations(preemptexpectations.New()))
			if err := manager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed adding ClusterQueue: %v", err)
			}
			if err := manager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("Failed adding LocalQueue: %v", err)
			}
			watcher.ProcessRequeues(ctx)
			if err := cl.Create(ctx, head.Obj); err != nil {
				t.Fatalf("Failed adding Workload to the client: %v", err)
			}
			cqImpl := manager.getClusterQueue("cq")
			// Increase the popCycle to ensure that the head will be added as inadmissible.
			cqImpl.popCycle++
			push := func(name, team string) {
				t.Helper()
				if err := manager.AddOrUpdateWorkload(ctx, log, newInfo(name, team).Obj); err != nil {
					t.Fatalf("Failed adding Workload %q: %v", name, err)
				}
			}
			push("team-a", "a")
			push("team-b", "b")
			manager.RequeueWorkload(ctx, head, RequeueReasonPreemptionNoCandidates, kueue.WorkloadQuotaReservedReasonWaitingForQuota)
			push("team-a-later", "a")
			push("team-b-later", "b")
			// Relabeled to a team the rule reads differently.
			push("team-b", "a")

			if diff := gocmp.Diff(tc.wantActive, manager.Dump(), cmpDump); diff != "" {
				t.Errorf("Unexpected active workloads (-want,+got):\n%s", diff)
			}
			if diff := gocmp.Diff(tc.wantInadmissible, manager.DumpInadmissible(), cmpDump); diff != "" {
				t.Errorf("Unexpected inadmissible workloads (-want,+got):\n%s", diff)
			}
			if reason, _ := cqImpl.GetNoFitReason("default/team-b-later"); reason != tc.wantNoFitReason {
				t.Errorf("GetNoFitReason() = %q, want %q", reason, tc.wantNoFitReason)
			}
		})
	}
}

// The first read of a PreemptionConfig starts its informer and waits for it to
// sync, so a requeue reads it without holding the Manager lock.
func TestRequeueWorkloadReadsPreemptionConfigWithoutLock(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
	features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, true)
	ctx, log := utiltesting.ContextWithLog(t)
	head := workload.NewInfo(log, utiltestingapi.MakeWorkload("head", defaultNamespace).Queue("lq").
		Request(corev1.ResourceCPU, "1").Obj())
	// Set by Heads when the scheduler pops the workload.
	head.ClusterQueue = "cq"
	var manager *Manager
	var read, readLocked bool
	cl := utiltesting.NewClientBuilder().
		WithObjects(utiltesting.MakeNamespace(defaultNamespace), head.Obj, kueuetestalpha1.MakePreemptionConfig("config").Obj()).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueuealpha.PreemptionConfig); ok {
					read = true
					if manager.TryLock() {
						manager.Unlock()
					} else {
						readLocked = true
					}
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	manager = NewManagerForUnitTests(cl, nil)
	if err := manager.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.BestEffortFIFO).
		Annotation(kueuealpha.PreemptionConfigNameAnnotation, "config").Obj()); err != nil {
		t.Fatalf("Failed adding ClusterQueue: %v", err)
	}
	if err := manager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", defaultNamespace).ClusterQueue("cq").Obj()); err != nil {
		t.Fatalf("Failed adding LocalQueue: %v", err)
	}

	manager.RequeueWorkload(ctx, head, RequeueReasonPreemptionNoCandidates, kueue.WorkloadQuotaReservedReasonWaitingForQuota)

	if !read {
		t.Fatal("RequeueWorkload did not read the PreemptionConfig")
	}
	if readLocked {
		t.Error("RequeueWorkload read the PreemptionConfig with the Manager lock held")
	}
}

// The PreemptionConfig is read only until the ClusterQueue has its
// PreemptorHashFields, and again once it references another PreemptionConfig.
func TestRequeueWorkloadReadsPreemptionConfigOncePerReference(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
	features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, true)
	ctx, log := utiltesting.ContextWithLog(t)
	reads := map[string]int{}
	cl := utiltesting.NewClientBuilder().
		WithObjects(utiltesting.MakeNamespace(defaultNamespace),
			kueuetestalpha1.MakePreemptionConfig("config").Obj(),
			kueuetestalpha1.MakePreemptionConfig("other").Obj()).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueuealpha.PreemptionConfig); ok {
					reads[key.Name]++
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	manager := NewManagerForUnitTests(cl, nil)
	cq := utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.BestEffortFIFO).
		Annotation(kueuealpha.PreemptionConfigNameAnnotation, "config").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding ClusterQueue: %v", err)
	}
	if err := manager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", defaultNamespace).ClusterQueue("cq").Obj()); err != nil {
		t.Fatalf("Failed adding LocalQueue: %v", err)
	}
	requeue := func(name string) {
		t.Helper()
		wl := utiltestingapi.MakeWorkload(name, defaultNamespace).Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
		if err := cl.Create(ctx, wl); err != nil {
			t.Fatalf("Failed adding Workload to the client: %v", err)
		}
		info := workload.NewInfo(log, wl)
		// Set by Heads when the scheduler pops the workload.
		info.ClusterQueue = "cq"
		manager.RequeueWorkload(ctx, info, RequeueReasonNoFit, kueue.WorkloadQuotaReservedReasonWaitingForQuota)
	}

	requeue("first")
	requeue("second")
	if diff := gocmp.Diff(map[string]int{"config": 1}, reads); diff != "" {
		t.Errorf("Unexpected PreemptionConfig reads (-want,+got):\n%s", diff)
	}

	updated := cq.DeepCopy()
	updated.Annotations[kueuealpha.PreemptionConfigNameAnnotation] = "other"
	if err := manager.UpdateClusterQueue(log, updated, false); err != nil {
		t.Fatalf("Failed to update ClusterQueue: %v", err)
	}
	requeue("third")
	requeue("fourth")
	if diff := gocmp.Diff(map[string]int{"config": 1, "other": 1}, reads); diff != "" {
		t.Errorf("Unexpected PreemptionConfig reads after switching the PreemptionConfig (-want,+got):\n%s", diff)
	}
}

// The rules evaluated a requeued workload with the values it had when it was
// popped. If a field they read changed since, the result says nothing about the
// workloads sharing its new scheduling hash, so the workload is retried and
// nothing is bulk-moved. A change of a field they do not read keeps the bulk move.
func TestRequeueWorkloadWithPreemptorHashFieldsChangedWhileInflight(t *testing.T) {
	cases := map[string]struct {
		relabel          map[string]string
		wantActive       map[kueue.ClusterQueueReference][]workload.Reference
		wantInadmissible map[kueue.ClusterQueueReference][]workload.Reference
	}{
		"a label read by the rules changed": {
			relabel:    map[string]string{"team": "a"},
			wantActive: map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/head", "default/team-a", "default/team-a-later", "default/team-b", "default/team-b-later"}},
		},
		"a label not read by the rules changed": {
			relabel:          map[string]string{"example.com/unrelated": "true"},
			wantActive:       map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/team-a", "default/team-a-later"}},
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/head", "default/team-b", "default/team-b-later"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, true)
			ctx, log := utiltesting.ContextWithLog(t)
			config := kueuetestalpha1.MakePreemptionConfig("config").
				Rules(kueuetestalpha1.MakePreemptionRule("rule", kueuealpha.Always,
					kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
				).PreemptorSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}).Obj()).
				Obj()
			newWorkload := func(name, team string) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, defaultNamespace).Queue("lq").
					Label("team", team).Request(corev1.ResourceCPU, "1").Obj()
			}
			cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace), config)
			manager, watcher := NewManagerForUnitTestsWithRequeuer(cl, nil, WithPreemptionExpectations(preemptexpectations.New()))
			if err := manager.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.BestEffortFIFO).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, "config").Obj()); err != nil {
				t.Fatalf("Failed adding ClusterQueue: %v", err)
			}
			if err := manager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", defaultNamespace).ClusterQueue("cq").Obj()); err != nil {
				t.Fatalf("Failed adding LocalQueue: %v", err)
			}
			watcher.ProcessRequeues(ctx)
			push := func(wl *kueue.Workload) {
				t.Helper()
				if err := manager.AddOrUpdateWorkload(ctx, log, wl); err != nil {
					t.Fatalf("Failed adding Workload %q: %v", wl.Name, err)
				}
			}
			headWl := newWorkload("head", "b")
			if err := cl.Create(ctx, headWl); err != nil {
				t.Fatalf("Failed adding Workload to the client: %v", err)
			}
			push(headWl)
			heads := manager.Heads(ctx)
			if len(heads) != 1 || heads[0].Obj.Name != "head" {
				t.Fatalf("Heads() = %v, want only the head", heads)
			}
			head := heads[0].Info
			push(newWorkload("team-a", "a"))
			push(newWorkload("team-b", "b"))
			// Relabeled while the scheduler evaluates it.
			relabeled := headWl.DeepCopy()
			maps.Copy(relabeled.Labels, tc.relabel)
			if err := cl.Update(ctx, relabeled); err != nil {
				t.Fatalf("Failed to relabel the Workload: %v", err)
			}
			push(relabeled)

			manager.RequeueWorkload(ctx, &head, RequeueReasonPreemptionNoCandidates, kueue.WorkloadQuotaReservedReasonWaitingForQuota)
			push(newWorkload("team-a-later", "a"))
			push(newWorkload("team-b-later", "b"))

			if diff := gocmp.Diff(tc.wantActive, manager.Dump(), cmpDump); diff != "" {
				t.Errorf("Unexpected active workloads (-want,+got):\n%s", diff)
			}
			if diff := gocmp.Diff(tc.wantInadmissible, manager.DumpInadmissible(), cmpDump); diff != "" {
				t.Errorf("Unexpected inadmissible workloads (-want,+got):\n%s", diff)
			}
		})
	}
}
