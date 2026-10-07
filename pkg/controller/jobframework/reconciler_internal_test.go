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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestDeferAdmissionCheckNodeSelectorsToPods(t *testing.T) {
	const (
		podSetName = kueue.PodSetReference("workers")
		flavorName = kueue.ResourceFlavorReference("flavor")
	)
	flavor := &kueue.ResourceFlavor{
		Spec: kueue.ResourceFlavorSpec{
			NodeLabels: map[string]string{
				"stable.example.com/node-pool": "workers",
				"shared.example.com/key":       "stable",
			},
		},
	}
	flavor.Name = string(flavorName)
	cl := utiltesting.NewClientBuilder(kueue.AddToScheme).WithObjects(flavor).Build()
	wl := &kueue.Workload{
		Spec: kueue.WorkloadSpec{
			PodSets: []kueue.PodSet{{
				Name:  podSetName,
				Count: 1,
			}},
		},
		Status: kueue.WorkloadStatus{
			Admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{{
					Name: podSetName,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: flavorName,
					},
				}},
			},
			AdmissionChecks: []kueue.AdmissionCheckState{{
				Name: "provisioning",
				PodSetUpdates: []kueue.PodSetUpdate{{
					Name: podSetName,
					NodeSelector: map[string]string{
						"request.example.com/id": "request-2",
						"shared.example.com/key": "stable",
					},
				}},
			}},
		},
	}
	info := []podset.PodSetInfo{{
		Name: podSetName,
		NodeSelector: map[string]string{
			"stable.example.com/node-pool": "workers",
			"shared.example.com/key":       "stable",
			"request.example.com/id":       "request-2",
		},
	}}

	if err := deferAdmissionCheckNodeSelectorsToPods(t.Context(), cl, wl, info); err != nil {
		t.Fatalf("deferAdmissionCheckNodeSelectorsToPods() error: %v", err)
	}

	want := map[string]string{
		"stable.example.com/node-pool": "workers",
		"shared.example.com/key":       "stable",
	}
	if diff := cmp.Diff(want, info[0].NodeSelector); diff != "" {
		t.Errorf("NodeSelector mismatch (-want,+got):\n%s", diff)
	}
}

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

func TestFinishReplacedWorkloadSlices(t *testing.T) {
	gvk := batchv1.SchemeGroupVersion.WithKind("Job")
	now := time.Now().Truncate(time.Second)
	parent := &batchv1.Job{Name: "job", Namespace: "ns", UID: "job-uid"}
	baseOld := utiltestingapi.MakeWorkload("old", "ns").UID("old-uid").ControllerReference(gvk, parent.Name, string(parent.UID))
	baseNew := utiltestingapi.MakeWorkload("new", "ns").UID("new-uid").ControllerReference(gvk, parent.Name, string(parent.UID)).Replaces("old", "old-uid")
	otherOwnerOld := baseOld.Clone()
	otherOwnerOld.OwnerReferences[0].UID = "another-job-uid"
	otherOwnerNew := baseNew.Clone()
	otherOwnerNew.OwnerReferences[0].UID = "another-job-uid"
	for name, tc := range map[string]struct {
		old          *kueue.Workload
		newSlice     *kueue.Workload
		wantFinished bool
	}{
		"intent alone does not finish predecessor":          {old: baseOld.Obj(), newSlice: utiltestingapi.MakeWorkload("new", "ns").UID("new-uid").ControllerReference(gvk, parent.Name, string(parent.UID)).Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/old").Obj()},
		"evicted successor retains replacement commitment":  {old: baseOld.Obj(), newSlice: baseNew.Clone().EvictedAt(now).Obj(), wantFinished: true},
		"finished successor retains replacement commitment": {old: baseOld.Obj(), newSlice: baseNew.Clone().FinishedAt(now).Obj(), wantFinished: true},
		"same name with different UID is ignored":           {old: baseOld.Clone().UID("different-uid").Obj(), newSlice: baseNew.Obj()},
		"predecessor owned by another job is ignored":       {old: otherOwnerOld.Obj(), newSlice: baseNew.Obj()},
		"successor from previous job instance is ignored":   {old: baseOld.Obj(), newSlice: otherOwnerNew.Obj()},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().WithObjects(parent, tc.old.DeepCopy(), tc.newSlice.DeepCopy()).WithStatusSubresource(&kueue.Workload{}).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(gvk), indexer.WorkloadOwnerIndexFunc(gvk)).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).Build()
			r := NewReconciler(cl, &utiltesting.EventRecorder{})
			if err := r.finishReplacedWorkloadSlices(ctx, parent, gvk); err != nil {
				t.Fatal(err)
			}
			got := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.old), got); err != nil {
				t.Fatal(err)
			}
			if workloadfinish.IsFinished(got) != tc.wantFinished {
				t.Errorf("finished = %t, want %t", workloadfinish.IsFinished(got), tc.wantFinished)
			}
		})
	}
}
