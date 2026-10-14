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
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
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

func TestGetPodSetsInfoFromStatusAdmissionCheckPodSetUpdates(t *testing.T) {
	const (
		podSetName        = kueue.PodSetReference("main")
		onDemandFlavor    = kueue.ResourceFlavorReference("on-demand")
		provisioningCheck = kueue.AdmissionCheckReference("provisioning")
	)
	now := time.Now().Truncate(time.Second)
	workloadGVK := kueue.SchemeGroupVersion.WithKind("Workload")
	admission := func(flavor kueue.ResourceFlavorReference) *kueue.Admission {
		return utiltestingapi.MakeAdmission("cq").
			PodSets(utiltestingapi.MakePodSetAssignment(podSetName).
				Assignment(corev1.ResourceCPU, flavor, "1").
				Obj()).
			Obj()
	}
	provisioningAnnotations := func(provisioningRequest string) map[string]string {
		return map[string]string{
			"autoscaling.x-k8s.io/consume-provisioning-request": provisioningRequest,
			"autoscaling.x-k8s.io/provisioning-class-name":      "queued-provisioning.gke.io",
		}
	}
	provisioningCheckState := func(provisioningRequest string) kueue.AdmissionCheckState {
		return kueue.AdmissionCheckState{
			Name:  provisioningCheck,
			State: kueue.CheckStateReady,
			PodSetUpdates: []kueue.PodSetUpdate{{
				Name:        podSetName,
				Annotations: provisioningAnnotations(provisioningRequest),
			}},
		}
	}
	baseWl := utiltestingapi.MakeWorkload("wl", "default").
		UID("wl-uid").
		PodSets(*utiltestingapi.MakePodSet(podSetName, 1).Obj()).
		ReserveQuotaAt(admission(onDemandFlavor), now).
		AdmittedAt(true, now)
	parentWl := baseWl.Clone().ParentVariant()
	variant := func(name string, flavor kueue.ResourceFlavorReference) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			ControllerReference(workloadGVK, "wl", "wl-uid").
			PodSets(*utiltestingapi.MakePodSet(podSetName, 1).Obj()).
			ReserveQuotaAt(admission(flavor), now)
	}
	podSetInfo := func(annotations map[string]string) []podset.PodSetInfo {
		return []podset.PodSetInfo{{
			Name:        podSetName,
			Count:       1,
			Annotations: annotations,
			Labels: map[string]string{
				constants.ClusterQueueLabel: "cq",
				constants.LocalQueueLabel:   "",
				constants.PodSetLabel:       string(podSetName),
			},
			NodeSelector: map[string]string{},
		}}
	}

	cases := map[string]struct {
		concurrentAdmission bool
		wl                  *kueue.Workload
		objs                []client.Object
		want                []podset.PodSetInfo
		wantErr             bool
	}{
		"uses the Workload admission checks when ConcurrentAdmission is disabled": {
			wl:   baseWl.Clone().AdmissionCheck(provisioningCheckState("wl-pr")).Obj(),
			want: podSetInfo(provisioningAnnotations("wl-pr")),
		},
		"uses the Workload admission checks for a Workload which is not a Parent": {
			concurrentAdmission: true,
			wl:                  baseWl.Clone().AdmissionCheck(provisioningCheckState("wl-pr")).Obj(),
			want:                podSetInfo(provisioningAnnotations("wl-pr")),
		},
		"fails when the Parent has no Variants": {
			concurrentAdmission: true,
			wl:                  parentWl.Clone().Obj(),
			wantErr:             true,
		},
		"uses the admission checks of the admitted Variant matching the Parent admission": {
			concurrentAdmission: true,
			wl:                  parentWl.Clone().Obj(),
			objs: []client.Object{
				variant("wl-dws", "dws").
					AdmissionCheck(provisioningCheckState("wl-dws-pr")).
					Obj(),
				variant("wl-on-demand", onDemandFlavor).
					AdmittedAt(true, now).
					AdmissionCheck(provisioningCheckState("wl-on-demand-pr")).
					Obj(),
			},
			want: podSetInfo(provisioningAnnotations("wl-on-demand-pr")),
		},
		"fails when no admitted Variant matches the Parent admission": {
			concurrentAdmission: true,
			wl:                  parentWl.Clone().Obj(),
			objs: []client.Object{
				variant("wl-dws", "dws").
					AdmittedAt(true, now).
					AdmissionCheck(provisioningCheckState("wl-dws-pr")).
					Obj(),
				variant("wl-on-demand", onDemandFlavor).
					AdmissionCheck(provisioningCheckState("wl-on-demand-pr")).
					Obj(),
			},
			wantErr: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConcurrentAdmission:         tc.concurrentAdmission,
				features.TopologyAwareScheduling:     false,
				features.SchedulerLibraryIntegration: false,
			})
			cl := utiltesting.NewClientBuilder(kueue.AddToScheme).
				WithObjects(utiltestingapi.MakeResourceFlavor(string(onDemandFlavor)).Obj()).
				WithObjects(tc.objs...).
				Build()

			got, err := getPodSetsInfoFromStatus(t.Context(), cl, tc.wl, false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("getPodSetsInfoFromStatus() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PodSetsInfo mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
