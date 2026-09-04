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

package jobframework

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestExpectedRunningPodSetsKeepsImplicitTASRequestInSync(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.TopologyAwareScheduling: true,
	})

	const podIndexLabel = "batch.kubernetes.io/job-completion-index"
	assignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
		Count(2).
		TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
			Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 2).Obj()).
			Obj()).
		Obj()
	wl := utiltestingapi.MakeWorkload("workload", "default").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
			PodIndexLabel(new(podIndexLabel)).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue").PodSets(assignment).Obj(), time.Now()).
		Obj()

	got := expectedRunningPodSets(t.Context(), utiltesting.NewClientBuilder().Build(), wl)
	want := []kueue.PodSet{
		*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
			Labels(map[string]string{
				constants.ClusterQueueLabel: "cluster-queue",
				constants.LocalQueueLabel:   "",
				constants.PodSetLabel:       string(kueue.DefaultPodSetName),
			}).
			Annotations(map[string]string{
				kueue.PodSetUnconstrainedTopologyAnnotation: "true",
				kueue.WorkloadAnnotation:                    "workload",
			}).
			NodeSelector(map[string]string{}).
			SchedulingGates(corev1.PodSchedulingGate{Name: kueue.TopologySchedulingGate}).
			UnconstrainedTopologyRequest().
			PodIndexLabel(new(podIndexLabel)).
			Obj(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("running PodSets (-want,+got):\n%s", diff)
	}
}

func TestClearUnusableMinCounts(t *testing.T) {
	podSetsWithMinCount := func() []kueue.PodSet {
		return []kueue.PodSet{
			*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Obj(),
		}
	}
	elasticWorkload := func() *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("wl", "default").
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
	}

	nonElastic := utiltestingapi.MakeWorkload("wl", "default").Obj()
	// The scale-up strategy annotation is set on the Job and is not propagated to the Workload, so
	// it cannot take part in this decision; opting in is enforced where MinCount is produced.
	elastic := elasticWorkload().Obj()

	cases := map[string]struct {
		partialAdmission     bool
		partialReplicaScale  bool
		wl                   *kueue.Workload
		wantMinCountsCleared bool
	}{
		"PartialAdmission enabled: minCount is kept regardless of elastic status": {
			partialAdmission:     true,
			wl:                   nonElastic,
			wantMinCountsCleared: false,
		},
		"both features disabled: minCount is cleared": {
			wl:                   nonElastic,
			wantMinCountsCleared: true,
		},
		"partial scale-up enabled, non-elastic workload: minCount is cleared": {
			partialReplicaScale:  true,
			wl:                   nonElastic,
			wantMinCountsCleared: true,
		},
		"partial scale-up enabled, elastic workload: minCount is kept": {
			partialReplicaScale:  true,
			wl:                   elastic,
			wantMinCountsCleared: false,
		},
		"elastic workload but partial scale-up disabled: minCount is cleared": {
			wl:                   elastic,
			wantMinCountsCleared: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:                          true,
				features.PartialAdmission:                                      tc.partialAdmission,
				features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: tc.partialReplicaScale,
			})

			got := clearUnusableMinCounts(podSetsWithMinCount(), tc.wl)

			for _, ps := range got {
				if cleared := ps.MinCount == nil; cleared != tc.wantMinCountsCleared {
					t.Errorf("podSet %q: minCount cleared = %v, want %v", ps.Name, cleared, tc.wantMinCountsCleared)
				}
			}
		})
	}
}
