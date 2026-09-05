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
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	rayjobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// rayJobGVK is used in tests below to represent a job type that actually
// supports the partial-scale-up-probe feature: per KEP-12100, only RayJob,
// RayService, and RayCluster support it (batch/v1 Job does not).
var rayJobGVK = rayv1.GroupVersion.WithKind("RayJob")

// newRayJobObject builds a RayJob object (via the shared rayjob test wrapper)
// standing in for a job type that supports the partial-scale-up-probe feature.
func newRayJobObject(name, ns, uid string, annotations map[string]string) *rayv1.RayJob {
	wrapper := rayjobtesting.MakeJob(name, ns)
	for k, v := range annotations {
		wrapper = wrapper.Annotation(k, v)
	}
	obj := wrapper.Obj()
	obj.UID = types.UID(uid)
	return obj
}

// fakeGenericJob is a minimal GenericJob used to exercise reconciler-internal
// helpers that only need Object() and GVK(); the remaining methods are never
// called by those helpers and simply return zero values.
type fakeGenericJob struct {
	obj client.Object
	gvk schema.GroupVersionKind
}

func (f *fakeGenericJob) Object() client.Object        { return f.obj }
func (f *fakeGenericJob) GVK() schema.GroupVersionKind { return f.gvk }
func (f *fakeGenericJob) IsSuspended() bool            { return false }
func (f *fakeGenericJob) Suspend()                     {}
func (f *fakeGenericJob) RunWithPodSetsInfo(context.Context, client.Client, []podset.PodSetInfo) error {
	return nil
}
func (f *fakeGenericJob) RestorePodSetsInfo(context.Context, []podset.PodSetInfo) bool { return false }
func (f *fakeGenericJob) Finished(context.Context) (string, bool, bool)                { return "", false, false }
func (f *fakeGenericJob) PodSets(context.Context, client.Client) ([]kueue.PodSet, error) {
	return nil, nil
}
func (f *fakeGenericJob) IsActive() bool                                { return false }
func (f *fakeGenericJob) PodsReady(context.Context, client.Client) bool { return false }

// fakeElasticWorkloadNameProviderJob additionally implements ElasticWorkloadNameProvider,
// mirroring RayJob/RayService.
type fakeElasticWorkloadNameProviderJob struct {
	*fakeGenericJob
	nameExtraPart string
}

func (f *fakeElasticWorkloadNameProviderJob) GetWorkloadNameExtraPart() string {
	return f.nameExtraPart
}

// TestNewWorkloadNameCombinesProviderAndScaleUpProbeExtra guards against
// ElasticWorkloadNameProvider (RayJob, RayService) silently discarding a
// caller-supplied scale-up-probe extra: since the provider's own extra is
// generation-based and does not change across successive partial admissions
// within the same generation, dropping the caller's extra would make repeated
// probes collide on the active workload's name instead of each requesting a
// distinct, larger admission.
func TestNewWorkloadNameCombinesProviderAndScaleUpProbeExtra(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)

	base := &fakeGenericJob{
		obj: newRayJobObject("rayjob-1", "ns", "rayjob-1-uid", map[string]string{
			workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
		}),
		gvk: rayJobGVK,
	}
	job := &fakeElasticWorkloadNameProviderJob{fakeGenericJob: base, nameExtraPart: "provider-gen-1"}

	nameNoProbe := newWorkloadName(job, "")
	nameProbeLevel3 := newWorkloadName(job, scaleUpProbeExtra+"-3")
	nameProbeLevel3Retry := newWorkloadName(job, scaleUpProbeExtra+"-3")
	nameProbeLevel5 := newWorkloadName(job, scaleUpProbeExtra+"-5")

	if nameProbeLevel3 == nameNoProbe {
		t.Errorf("expected scale-up-probe name to differ from the no-probe (provider-only) name, both were %q", nameProbeLevel3)
	}
	if nameProbeLevel3 != nameProbeLevel3Retry {
		t.Errorf("expected retry at the same admitted level to reuse the same name, got %q and %q", nameProbeLevel3, nameProbeLevel3Retry)
	}
	if nameProbeLevel3 == nameProbeLevel5 {
		t.Errorf("expected the name to change once the admitted level progresses, got %q for both", nameProbeLevel3)
	}

	wantNameNoProbe := GenerateWorkloadNameWithExtra(job.Object().GetName(), job.Object().GetUID(), job.GVK(), "provider-gen-1")
	if nameNoProbe != wantNameNoProbe {
		t.Errorf("nameNoProbe = %q, want %q", nameNoProbe, wantNameNoProbe)
	}
	wantNameProbeLevel3 := GenerateWorkloadNameWithExtra(job.Object().GetName(), job.Object().GetUID(), job.GVK(), "provider-gen-1-"+scaleUpProbeExtra+"-3")
	if nameProbeLevel3 != wantNameProbeLevel3 {
		t.Errorf("nameProbeLevel3 = %q, want %q", nameProbeLevel3, wantNameProbeLevel3)
	}
}

func TestShouldCreatePartialScaleUpProbe(t *testing.T) {
	sliceEnabledAnnotations := map[string]string{
		workloadslicing.EnabledAnnotationKey:             workloadslicing.EnabledAnnotationValue,
		constants.ElasticJobScaleUpStrategyAnnotationKey: constants.ElasticJobScaleUpStrategyPartial,
	}

	cases := map[string]struct {
		sliceFeatureEnabled   bool
		partialFeatureEnabled bool
		annotations           map[string]string
		want                  bool
	}{
		"all gates satisfied": {
			sliceFeatureEnabled:   true,
			partialFeatureEnabled: true,
			annotations:           sliceEnabledAnnotations,
			want:                  true,
		},
		"workload slicing feature gate disabled": {
			sliceFeatureEnabled:   false,
			partialFeatureEnabled: true,
			annotations:           sliceEnabledAnnotations,
			want:                  false,
		},
		"workload slicing annotation missing": {
			sliceFeatureEnabled:   true,
			partialFeatureEnabled: true,
			annotations: map[string]string{
				constants.ElasticJobScaleUpStrategyAnnotationKey: constants.ElasticJobScaleUpStrategyPartial,
			},
			want: false,
		},
		"partial scale-up feature gate disabled": {
			sliceFeatureEnabled:   true,
			partialFeatureEnabled: false,
			annotations:           sliceEnabledAnnotations,
			want:                  false,
		},
		"scale-up strategy annotation missing": {
			sliceFeatureEnabled:   true,
			partialFeatureEnabled: true,
			annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
			},
			want: false,
		},
		"scale-up strategy annotation has unrecognized value": {
			sliceFeatureEnabled:   true,
			partialFeatureEnabled: true,
			annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey:             workloadslicing.EnabledAnnotationValue,
				constants.ElasticJobScaleUpStrategyAnnotationKey: "bogus",
			},
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:                          tc.sliceFeatureEnabled,
				features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: tc.partialFeatureEnabled,
			})
			// KEP-12100 scopes the partial-scale-up-probe path to RayJob/RayService/
			// RayCluster, so exercise it exclusively through a RayJob-shaped fixture.
			job := &fakeGenericJob{
				obj: newRayJobObject("rayjob-1", "ns", "rayjob-1-uid", tc.annotations),
				gvk: rayJobGVK,
			}
			if got := shouldCreatePartialScaleUpProbe(job); got != tc.want {
				t.Errorf("shouldCreatePartialScaleUpProbe() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPrepareWorkloadSliceForScaleUpNaming verifies that the workload name
// returned for a partial scale-up probe is stable across retries at the same
// admitted level, and changes once the admitted level progresses.
func TestPrepareWorkloadSliceForScaleUpNaming(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)

	ctx, _ := utiltesting.ContextWithLog(t)
	gvk := rayJobGVK
	now := time.Now()

	jobObj := newRayJobObject("rayjob-naming", "ns", "rayjob-naming-uid", map[string]string{
		workloadslicing.EnabledAnnotationKey:             workloadslicing.EnabledAnnotationValue,
		constants.ElasticJobScaleUpStrategyAnnotationKey: constants.ElasticJobScaleUpStrategyPartial,
	})
	job := &fakeGenericJob{obj: jobObj, gvk: gvk}

	podSets := []kueue.PodSet{
		{Name: kueue.PodSetReference("workers"), Count: 10},
	}

	makePrevWl := func(admittedCount int32) *kueue.Workload {
		return utiltestingapi.MakeWorkload("job-naming-prev", "ns").
			PodSets(kueue.PodSet{Name: kueue.PodSetReference("workers"), Count: 10}).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
				utiltestingapi.MakePodSetAssignment(kueue.PodSetReference("workers")).
					Assignment(corev1.ResourceCPU, "default", "1").
					Count(admittedCount).
					Obj(),
			).Obj(), now).
			ControllerReference(gvk, "rayjob-naming", "rayjob-naming-uid").
			Obj()
	}

	newClient := func(prevWl *kueue.Workload) client.Client {
		return utiltesting.NewClientBuilder().
			WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}, prevWl).
			WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(gvk), indexer.WorkloadOwnerIndexFunc(gvk)).
			Build()
	}

	prevWlLevel3 := makePrevWl(3)
	extraLevel3First, err := prepareWorkloadSliceForScaleUp(ctx, newClient(prevWlLevel3), job, append([]kueue.PodSet{}, podSets...))
	if err != nil {
		t.Fatalf("prepareWorkloadSliceForScaleUp() error: %v", err)
	}

	// A retry at the same admitted level must reuse the exact same extra/name.
	extraLevel3Retry, err := prepareWorkloadSliceForScaleUp(ctx, newClient(prevWlLevel3), job, append([]kueue.PodSet{}, podSets...))
	if err != nil {
		t.Fatalf("prepareWorkloadSliceForScaleUp() error: %v", err)
	}
	if extraLevel3First != extraLevel3Retry {
		t.Errorf("expected idempotent extra at unchanged admitted level, got %q and %q", extraLevel3First, extraLevel3Retry)
	}
	wantLevel3 := scaleUpProbeExtra + "-3"
	if extraLevel3First != wantLevel3 {
		t.Errorf("extra = %q, want %q", extraLevel3First, wantLevel3)
	}

	// Once the admitted level progresses, the extra/name must change.
	prevWlLevel5 := makePrevWl(5)
	extraLevel5, err := prepareWorkloadSliceForScaleUp(ctx, newClient(prevWlLevel5), job, append([]kueue.PodSet{}, podSets...))
	if err != nil {
		t.Fatalf("prepareWorkloadSliceForScaleUp() error: %v", err)
	}
	wantLevel5 := scaleUpProbeExtra + "-5"
	if extraLevel5 != wantLevel5 {
		t.Errorf("extra = %q, want %q", extraLevel5, wantLevel5)
	}
	if extraLevel5 == extraLevel3First {
		t.Errorf("expected extra to change across admitted levels, got %q for both", extraLevel5)
	}

	nameLevel3 := GenerateWorkloadNameWithExtra(jobObj.GetName(), jobObj.GetUID(), gvk, extraLevel3First)
	nameLevel5 := GenerateWorkloadNameWithExtra(jobObj.GetName(), jobObj.GetUID(), gvk, extraLevel5)
	if nameLevel3 == nameLevel5 {
		t.Errorf("expected generated workload names to differ across admitted levels, both were %q", nameLevel3)
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
