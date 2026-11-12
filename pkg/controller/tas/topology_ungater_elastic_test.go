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

package tas

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/elasticjobs"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestUngatersElasticReplacements(t *testing.T) {
	for name, tc := range map[string]struct {
		indexed       bool
		topologyFirst bool
		withTAS       bool
	}{
		"indexed, elastic first":                   {indexed: true, withTAS: true},
		"indexed, topology first":                  {indexed: true, topologyFirst: true, withTAS: true},
		"indexed, elastic first, no TAS slice":     {indexed: true},
		"indexed, topology first, no TAS slice":    {indexed: true, topologyFirst: true},
		"name order, elastic first":                {withTAS: true},
		"name order, topology first":               {topologyFirst: true, withTAS: true},
		"name order, elastic first, no TAS slice":  {},
		"name order, topology first, no TAS slice": {topologyFirst: true},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, tc.withTAS)
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			ps := utiltestingapi.MakePodSet("workers", 2).UnconstrainedTopologyRequest()
			if tc.indexed {
				ps.PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation))
			}
			wl := utiltestingapi.MakeWorkload("wl", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*ps.Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
					utiltestingapi.MakePodSetAssignment("workers").Count(2).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{tasRackLabel}).Domains(
							utiltestingapi.MakeTopologyDomainAssignment([]string{"r1"}, 1).Obj(),
							utiltestingapi.MakeTopologyDomainAssignment([]string{"r2"}, 1).Obj(),
						).Obj()).Obj(),
				).Obj(), now).AdmittedAt(true, now).Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(wl).
				WithStatusSubresource(wl, &corev1.Pod{}).
				WithIndex(&corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName).
				WithIndex(&kueue.Workload{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if err := c.List(ctx, list, opts...); err != nil {
							return err
						}
						// API list order need not match elastic admission's name order.
						if pods, ok := list.(*corev1.PodList); ok {
							slices.Reverse(pods.Items)
						}
						return nil
					},
				}).Build()
			basePod := testingpod.MakePod("", "ns").
				Annotation(kueue.WorkloadAnnotation, wl.Name).
				Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				Label(constants.PodSetLabel, "workers").
				Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate)
			podSpecs := []struct {
				name  string
				index string
			}{
				{name: "rank-0", index: "0"},
				{name: "rank-1", index: "1"},
				{name: "rank-2", index: "2"},
				// Name rank-0's replacement after rank-1's so indexed (rank) and unindexed (name) ordering diverge.
				{name: "replacement-b", index: "0"},
				{name: "replacement-a", index: "1"},
			}
			pods := make([]*corev1.Pod, 0, len(podSpecs))
			for i, spec := range podSpecs {
				builder := basePod.Clone().Name(spec.name)
				if tc.indexed {
					builder = builder.Label(batchv1.JobCompletionIndexAnnotation, spec.index)
				}
				pod := builder.Obj()
				pod.UID = types.UID(strconv.Itoa(i))
				pods = append(pods, pod)
			}
			for _, pod := range pods[:2] {
				if err := cl.Create(ctx, pod); err != nil {
					t.Fatal(err)
				}
			}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}
			rankZeroDomain, rankOneDomain := "r1", "r2"
			firstReplacement, secondReplacement := "replacement-b", "replacement-a"
			if !tc.indexed {
				// Greedy topology placement follows the reversed list order; name order picks replacement-a before replacement-b.
				rankZeroDomain, rankOneDomain = "r2", "r1"
				firstReplacement, secondReplacement = "replacement-a", "replacement-b"
			}
			stages := []struct {
				name         string
				scale        bool
				failed       *corev1.Pod
				created      *corev1.Pod
				wantRunnable map[string]string
			}{
				{name: "admit two workers", wantRunnable: map[string]string{"rank-0": rankZeroDomain, "rank-1": rankOneDomain}},
				{name: "scale to three with admission unchanged", scale: true, created: pods[2], wantRunnable: map[string]string{"rank-0": rankZeroDomain, "rank-1": rankOneDomain}},
				{name: "rank two borrows failed rank zero's domain", failed: pods[0], wantRunnable: map[string]string{"rank-2": rankZeroDomain, "rank-1": rankOneDomain}},
				{name: "rank zero replacement waits for admission", created: pods[3], wantRunnable: map[string]string{"rank-2": rankZeroDomain, "rank-1": rankOneDomain}},
				{name: "replacements compete for the remaining slot", failed: pods[1], created: pods[4], wantRunnable: map[string]string{"rank-2": rankZeroDomain, firstReplacement: rankOneDomain}},
				{name: "waiting replacement progresses when capacity returns", failed: pods[2], wantRunnable: map[string]string{secondReplacement: rankZeroDomain, firstReplacement: rankOneDomain}},
			}
			for _, stage := range stages {
				t.Run(stage.name, func(t *testing.T) {
					if stage.scale {
						pendingSlice := utiltestingapi.MakeWorkload("wl-scaled", "ns").
							Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
							Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl))).
							Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
							PodSets(*utiltestingapi.MakePodSet("workers", 3).UnconstrainedTopologyRequest().Obj()).Obj()
						if tc.indexed {
							pendingSlice.Spec.PodSets[0].TopologyRequest.PodIndexLabel = new(batchv1.JobCompletionIndexAnnotation)
						}
						if err := cl.Create(ctx, pendingSlice); err != nil {
							t.Fatal(err)
						}
					}
					if stage.failed != nil {
						if err := cl.Get(ctx, client.ObjectKeyFromObject(stage.failed), stage.failed); err != nil {
							t.Fatal(err)
						}
						stage.failed.Status.Phase = corev1.PodFailed
						if err := cl.Status().Update(ctx, stage.failed); err != nil {
							t.Fatal(err)
						}
					}
					if stage.created != nil {
						if err := cl.Create(ctx, stage.created); err != nil {
							t.Fatal(err)
						}
					}
					// Each stage starts from a fully observed snapshot. Event-handler
					// expectation tracking is covered separately by the controller tests.
					elastic := elasticjobs.NewUngater(cl, nil, nil)
					topology := newTopologyUngater(cl, nil, nil)
					order := []reconcile.Reconciler{elastic, topology}
					if tc.topologyFirst {
						order = []reconcile.Reconciler{topology, elastic, topology}
					}
					for _, r := range order {
						if _, err := r.Reconcile(ctx, req); err != nil {
							t.Fatalf("Reconcile: %v", err)
						}
						var list corev1.PodList
						if err := cl.List(ctx, &list); err != nil {
							t.Fatal(err)
						}
						admitted := 0
						domains := make(map[string]int)
						for _, pod := range list.Items {
							if utilpod.IsTerminated(&pod) {
								continue
							}
							if !utilpod.HasGate(&pod, kueue.ElasticJobSchedulingGate) {
								admitted++
							}
							if !utilpod.HasGate(&pod, kueue.TopologySchedulingGate) {
								domains[pod.Spec.NodeSelector[tasRackLabel]]++
								// The fake client exposes the patch immediately; simulate
								// the Pod event before the next topology reconciliation.
								topology.expectationsStore.ObservedUID(log, req.NamespacedName, pod.UID)
							}
						}
						if admitted > 2 || domains["r1"] > 1 || domains["r2"] > 1 || domains[""] > 0 {
							t.Fatalf("capacity exceeded after %T: admitted=%d, domains=%v", r, admitted, domains)
						}
					}
					runnable := runnablePodDomains(ctx, t, cl)
					if diff := cmp.Diff(stage.wantRunnable, runnable); diff != "" {
						t.Fatalf("runnable pods (-want,+got):\n%s", diff)
					}
				})
				if t.Failed() {
					break
				}
			}
		})
	}
}

func TestUngatersElasticAdmissionAfterDomainRemoval(t *testing.T) {
	for name, tc := range map[string]struct {
		topologyFirst bool
		withTAS       bool
	}{
		"elastic first":                {withTAS: true},
		"topology first":               {topologyFirst: true, withTAS: true},
		"elastic first, no TAS slice":  {},
		"topology first, no TAS slice": {topologyFirst: true},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, tc.withTAS)
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			// A mixed resize can truncate this PodSet's assignment before the job
			// controller removes the surviving worker in the dropped domain.
			wl := utiltestingapi.MakeWorkload("wl", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("workers", 1).UnconstrainedTopologyRequest().
					PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
					utiltestingapi.MakePodSetAssignment("workers").Count(1).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{tasRackLabel}).Domains(
							utiltestingapi.MakeTopologyDomainAssignment([]string{"r1"}, 1).Obj(),
						).Obj()).Obj(),
				).Obj(), now).AdmittedAt(true, now).Obj()
			basePod := testingpod.MakePod("", "ns").
				Annotation(kueue.WorkloadAnnotation, wl.Name).
				Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				Label(constants.PodSetLabel, "workers")
			survivor := basePod.Clone().Name("rank-1").UID("survivor").
				Label(batchv1.JobCompletionIndexAnnotation, "1").
				NodeSelector(tasRackLabel, "r2").StatusPhase(corev1.PodRunning).Obj()
			replacement := basePod.Clone().Name("replacement-0").UID("replacement").
				Label(batchv1.JobCompletionIndexAnnotation, "0").
				Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(wl, survivor, replacement).
				WithStatusSubresource(&corev1.Pod{}).
				WithIndex(&corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName).
				WithIndex(&kueue.Workload{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexWorkloadSliceName).Build()
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}
			stages := []struct {
				name          string
				survivorPhase corev1.PodPhase
				wantRunnable  map[string]string
			}{
				{
					name:          "running survivor consumes admission slot",
					survivorPhase: corev1.PodRunning,
					wantRunnable:  map[string]string{survivor.Name: survivor.Spec.NodeSelector[tasRackLabel]},
				},
				{
					name:          "failed survivor frees slot for replacement in r1",
					survivorPhase: corev1.PodFailed,
					wantRunnable:  map[string]string{replacement.Name: "r1"},
				},
			}
			for _, stage := range stages {
				t.Run(stage.name, func(t *testing.T) {
					survivor.Status.Phase = stage.survivorPhase
					if err := cl.Status().Update(ctx, survivor); err != nil {
						t.Fatal(err)
					}
					elastic := elasticjobs.NewUngater(cl, nil, nil)
					topology := newTopologyUngater(cl, nil, nil)
					order := []reconcile.Reconciler{elastic, topology}
					if tc.topologyFirst {
						order = []reconcile.Reconciler{topology, elastic, topology}
					}
					for _, r := range order {
						if _, err := r.Reconcile(ctx, req); err != nil {
							t.Fatalf("Reconcile: %v", err)
						}
						if err := cl.Get(ctx, client.ObjectKeyFromObject(replacement), replacement); err != nil {
							t.Fatal(err)
						}
						if stage.survivorPhase == corev1.PodRunning && !utilpod.HasGate(replacement, kueue.ElasticJobSchedulingGate) {
							t.Fatalf("replacement exceeded admission after %T while survivor is running", r)
						}
					}
					runnable := runnablePodDomains(ctx, t, cl)
					if diff := cmp.Diff(stage.wantRunnable, runnable); diff != "" {
						t.Fatalf("runnable pods (-want,+got):\n%s", diff)
					}
				})
			}
		})
	}
}

func runnablePodDomains(ctx context.Context, t *testing.T, cl client.Reader) map[string]string {
	t.Helper()
	var list corev1.PodList
	if err := cl.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	runnable := make(map[string]string)
	for _, pod := range list.Items {
		if !utilpod.IsTerminated(&pod) && len(pod.Spec.SchedulingGates) == 0 {
			runnable[pod.Name] = pod.Spec.NodeSelector[tasRackLabel]
		}
	}
	return runnable
}
