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

package policy

import (
	"cmp"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCandidatesOrderingPartialPreemption(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
	_, log := utiltesting.ContextWithLog(t)
	now := time.Now()

	const cq kueue.ClusterQueueReference = "cq"

	makeInfo := func(name string, priority int32, partial bool) *workload.Info {
		wl := utiltestingapi.MakeWorkload(name, metav1.NamespaceDefault).
			Queue("default").
			Priority(priority).
			PodSets(*utiltestingapi.MakePodSet("main", 5).
				SetMinimumCount(2).
				Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		if partial {
			wl.Annotations = map[string]string{
				constants.ElasticJobAnnotation:        "true",
				constants.PartialPreemptionAnnotation: "true",
			}
		}
		info := workload.NewInfo(log, wl)
		info.ClusterQueue = cq
		return info
	}

	testCases := map[string]struct {
		a, b *workload.Info
		want int
	}{
		"partial-preemptible sorts before non-elastic (equal priority)": {
			a:    makeInfo("partial", 100, true),
			b:    makeInfo("whole", 100, false),
			want: -1,
		},
		"non-elastic sorts after partial-preemptible (equal priority)": {
			a:    makeInfo("whole", 100, false),
			b:    makeInfo("partial", 100, true),
			want: 1,
		},
		"lower priority wins before the partial-preemption criterion": {
			a:    makeInfo("whole-low-pri", 50, false),
			b:    makeInfo("partial-high-pri", 100, true),
			want: -1,
		},
		"both partial-preemptible falls through to lower priority first": {
			a:    makeInfo("partial-low-pri", 50, true),
			b:    makeInfo("partial-high-pri", 100, true),
			want: -1,
		},
		"both non-elastic falls through to lower priority first": {
			a:    makeInfo("whole-low-pri", 50, false),
			b:    makeInfo("whole-high-pri", 100, false),
			want: -1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := CandidatesOrdering(log, false, tc.a, tc.b, cq, now)
			if cmp.Compare(got, 0) != tc.want {
				t.Errorf("CandidatesOrdering() = %d (sign %d), want sign %d", got, cmp.Compare(got, 0), tc.want)
			}
		})
	}
}

func TestCandidatesOrderingPartialPreemptionGateDisabled(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, false)
	_, log := utiltesting.ContextWithLog(t)
	now := time.Now()
	const cq kueue.ClusterQueueReference = "cq"

	annotated := utiltestingapi.MakeWorkload("annotated", metav1.NamespaceDefault).
		Queue("default").
		Priority(100).
		Annotation(constants.PartialPreemptionAnnotation, "true").
		PodSets(*utiltestingapi.MakePodSet("main", 5).SetMinimumCount(2).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()
	plain := utiltestingapi.MakeWorkload("plain", metav1.NamespaceDefault).
		Queue("default").
		Priority(100).
		PodSets(*utiltestingapi.MakePodSet("main", 5).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()

	aInfo := workload.NewInfo(log, annotated)
	aInfo.ClusterQueue = cq
	bInfo := workload.NewInfo(log, plain)
	bInfo.ClusterQueue = cq

	if aInfo.IsPartialPreemptionJob {
		t.Fatalf("IsPartialPreemptionJob should be false when the gate is disabled")
	}

	ab := CandidatesOrdering(log, false, aInfo, bInfo, cq, now)
	ba := CandidatesOrdering(log, false, bInfo, aInfo, cq, now)
	if cmp.Compare(ab, 0) != -cmp.Compare(ba, 0) {
		t.Errorf("ordering not antisymmetric: ab=%d ba=%d", ab, ba)
	}
}

func TestCandidatesOrderingUsedCount(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)

	_, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)
	const cq kueue.ClusterQueueReference = "cq"

	partialWl := func(name string, preemptionMinCount int32) *workload.Info {
		wl := utiltestingapi.MakeWorkload(name, metav1.NamespaceDefault).
			UID(types.UID(name)).
			Queue("default").
			Annotation(constants.PartialPreemptionAnnotation, "true").
			Priority(100).
			PodSets(*utiltestingapi.MakePodSet("executor", 5).
				Request(corev1.ResourceCPU, "1").
				SetPreemptionMinimumCount(preemptionMinCount).
				Obj()).
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment("executor").
						Assignment(corev1.ResourceCPU, "default", "1").
						Count(5).
						Obj()).
					Obj(),
				now,
			).
			Obj()
		info := workload.NewInfo(log, wl)
		info.ClusterQueue = cq
		return info
	}

	a := partialWl("a", 1)
	b := partialWl("b", 4)

	if a.PartialPreemptionUsedCount != 4 || b.PartialPreemptionUsedCount != 1 {
		t.Fatalf("cached used counts = (%d, %d), want (4, 1)", a.PartialPreemptionUsedCount, b.PartialPreemptionUsedCount)
	}
	if got := CandidatesOrdering(log, false, a, b, cq, now); cmp.Compare(got, 0) != -1 {
		t.Errorf("CandidatesOrdering(a, b) = %d (sign %d), want sign -1 (more sheddable first)", got, cmp.Compare(got, 0))
	}
	if got := CandidatesOrdering(log, false, b, a, cq, now); cmp.Compare(got, 0) != 1 {
		t.Errorf("CandidatesOrdering(b, a) = %d (sign %d), want sign 1 (antisymmetry)", got, cmp.Compare(got, 0))
	}
}
