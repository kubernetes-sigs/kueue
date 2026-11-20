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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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

// sliceTestJob implements only the GenericJob methods that admittedSliceReplacedByProbe calls.
type sliceTestJob struct {
	GenericJob
	obj *batchv1.Job
}

func (j *sliceTestJob) Object() client.Object {
	return j.obj
}

func (j *sliceTestJob) GVK() schema.GroupVersionKind {
	return batchv1.SchemeGroupVersion.WithKind("Job")
}

func TestAdmittedSliceReplacedByProbe(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	gvk := batchv1.SchemeGroupVersion.WithKind("Job")
	job := &batchv1.Job{}
	job.Name = "job"
	job.Namespace = "ns"
	job.UID = "job-uid"
	job.Annotations = map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue}
	probe := utiltestingapi.MakeWorkload("probe", "ns").
		ControllerReference(gvk, job.Name, string(job.UID)).
		Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/admitted").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Obj()).
		Obj()
	admitted := func() *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("admitted", "ns").
			ControllerReference(gvk, job.Name, string(job.UID)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 7).Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
			AdmittedAt(true, now)
	}

	cases := map[string]struct {
		predecessor *kueue.Workload
		want        string
	}{
		"admitted predecessor: the job starts on it": {
			predecessor: admitted().Obj(),
			want:        "admitted",
		},
		// The job must not start on a slice that is being deleted.
		"terminating predecessor: the probe is kept": {
			predecessor: admitted().Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
			want:        "probe",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithObjects(job, probe.DeepCopy(), tc.predecessor).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(gvk), indexer.WorkloadOwnerIndexFunc(gvk)).
				Build()
			r := &JobReconciler{client: cl}
			got, err := r.admittedSliceReplacedByProbe(ctx, &sliceTestJob{obj: job}, probe.DeepCopy())
			if err != nil {
				t.Fatalf("admittedSliceReplacedByProbe() error = %v", err)
			}
			if got.Name != tc.want {
				t.Errorf("admittedSliceReplacedByProbe() = %q, want %q", got.Name, tc.want)
			}
		})
	}
}
