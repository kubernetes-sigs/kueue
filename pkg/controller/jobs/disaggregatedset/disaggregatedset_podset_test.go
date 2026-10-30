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

package disaggregatedset

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
)

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		ds           *disaggregatedsetv1.DisaggregatedSet
		featureGates map[featuregate.Feature]bool
		want         []kueue.PodSet
	}{
		"roles with and without a leader template": {
			ds: testingds.MakeDisaggregatedSet("ds", "ns").
				Roles(
					testingds.MakeRole("prefill").Replicas(3).Size(2),
					testingds.MakeRole("router").WithLeader().Replicas(2),
					testingds.MakeRole("decode").WithLeader().Replicas(2).Size(4),
				).
				Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("prefill", 6).Obj(),
				*utiltestingapi.MakePodSet("router-leader", 2).Obj(),
				*utiltestingapi.MakePodSet("decode-leader", 2).Obj(),
				*utiltestingapi.MakePodSet("decode-worker", 6).Obj(),
			},
		},
		"LeaderWorkerSet and DisaggregatedSet keys are dropped": {
			ds: testingds.MakeDisaggregatedSet("ds", "ns").
				Roles(testingds.MakeRole("prefill").
					WorkerTemplateLabel(disaggregatedsetv1.RoleLabelKey, "prefill").
					WorkerTemplateLabel("app", "vllm").
					WorkerTemplateAnnotation("leaderworkerset.sigs.k8s.io/exclusive-topology", "rack")).
				Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("prefill", 1).Labels(map[string]string{"app": "vllm"}).Obj(),
			},
		},
		"TAS PodSet group is named after the role": {
			ds: testingds.MakeDisaggregatedSet("ds", "ns").
				Roles(
					testingds.MakeRole("prefill").WithLeader().Size(3).
						PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
						PodTemplateAnnotation(kueue.PodSetGroupName, "group"),
					testingds.MakeRole("decode").WithLeader().Size(3).
						PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
						PodTemplateAnnotation(kueue.PodSetGroupName, "group"),
				).
				Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("prefill-leader", 1).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block", kueue.PodSetGroupName: "group"}).
					RequiredTopologyRequest("cloud.com/block").PodSetGroup("role-prefill").Obj(),
				*utiltestingapi.MakePodSet("prefill-worker", 2).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block", kueue.PodSetGroupName: "group"}).
					RequiredTopologyRequest("cloud.com/block").PodSetGroup("role-prefill").Obj(),
				*utiltestingapi.MakePodSet("decode-leader", 1).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block", kueue.PodSetGroupName: "group"}).
					RequiredTopologyRequest("cloud.com/block").PodSetGroup("role-decode").Obj(),
				*utiltestingapi.MakePodSet("decode-worker", 2).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block", kueue.PodSetGroupName: "group"}).
					RequiredTopologyRequest("cloud.com/block").PodSetGroup("role-decode").Obj(),
			},
		},
		"TAS disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			ds: testingds.MakeDisaggregatedSet("ds", "ns").
				Roles(testingds.MakeRole("prefill").WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block")).
				Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("prefill", 1).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).Obj(),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			got, err := podSets(tc.ds)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			// The Pod spec is copied unchanged; the cases check the metadata.
			opts := []cmp.Option{cmpopts.IgnoreFields(corev1.PodTemplateSpec{}, "Spec"), cmpopts.EquateEmpty()}
			if diff := cmp.Diff(tc.want, got, opts...); diff != "" {
				t.Errorf("Unexpected PodSets (-want,+got):\n%s", diff)
			}
		})
	}
}

// TestProjectedWorkloadBytes calibrates the constants of projectedWorkloadBytes.
// It serializes the largest slice Workloads that the webhook accepts and checks
// that the estimate is an upper bound and stays below the object size limit.
// The Workloads have the most PodSets, role templates at the size limit, one
// owner reference per Pod with its managed-fields entry, and, with TAS, one
// domain per Pod at every level of a Topology with the most levels. Names and
// values are 63-character hashes, so that the compact TAS format cannot share
// a prefix or suffix between them.
func TestProjectedWorkloadBytes(t *testing.T) {
	const (
		objectSizeLimit = 1536 * 1024
		podSetCount     = 18
		topologyLevels  = 16
	)
	if safeWorkloadBytes >= objectSizeLimit {
		t.Fatalf("safeWorkloadBytes %d must be below the object size limit %d", safeWorkloadBytes, objectSizeLimit)
	}
	testCases := map[string]struct {
		membership int
		withTAS    bool
	}{
		"without TAS": {
			membership: maxSliceMembership,
		},
		"with TAS": {
			// The largest accepted membership with TAS, for templates at the limit.
			membership: (safeWorkloadBytes - workloadHeadroomBytes - maxTemplateBytes) / (ownerBytes + tasBytesPerPod),
			withTAS:    true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var levels []string
			for l := range topologyLevels {
				levels = append(levels, fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "level-%d", l)))[:63])
			}

			var podSets []kueue.PodSet
			var assignments []kueue.PodSetAssignment
			var templates, pod int
			for i := range podSetCount {
				count := tc.membership / podSetCount
				if i < tc.membership%podSetCount {
					count++
				}
				podSetName := kueue.PodSetReference(fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "podset-%d", i)))[:63])
				podSet := utiltestingapi.MakePodSet(podSetName, count).
					Image(fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "image-%d", i)))[:63]).
					Annotations(map[string]string{"p": ""}).
					Obj()
				base, err := json.Marshal(podSet.Template)
				if err != nil {
					t.Fatalf("Failed to marshal the template: %v", err)
				}
				podSet.Template.Annotations["p"] = strings.Repeat("x", maxTemplateBytes/podSetCount-len(base))
				b, err := json.Marshal(podSet.Template)
				if err != nil {
					t.Fatalf("Failed to marshal the template: %v", err)
				}
				templates += len(b)
				podSets = append(podSets, *podSet)

				assignment := utiltestingapi.MakePodSetAssignment(podSetName).
					Count(int32(count)).
					Flavor(corev1.ResourceCPU, kueue.ResourceFlavorReference(fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "flavor-%d", i)))[:63]))
				if tc.withTAS {
					topologyAssignment := utiltestingapi.MakeTopologyAssignment(levels)
					for range count {
						var values []string
						for l := range topologyLevels {
							values = append(values, fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "domain-%d-%d", pod, l)))[:63])
						}
						topologyAssignment.Domain(utiltestingapi.MakeTopologyDomainAssignment(values, 1).Obj())
						pod++
					}
					assignment.TopologyAssignment(topologyAssignment.Obj())
				}
				assignments = append(assignments, assignment.Obj())
			}
			if templates > maxTemplateBytes {
				t.Fatalf("Templates use %d bytes, above the limit %d", templates, maxTemplateBytes)
			}

			wl := utiltestingapi.MakeWorkload(
				fmt.Sprintf("%x", sha256.Sum256([]byte("workload")))[:63],
				fmt.Sprintf("%x", sha256.Sum256([]byte("namespace")))[:63],
			).
				PodSets(podSets...).
				Admission(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(fmt.Sprintf("%x", sha256.Sum256([]byte("cq")))[:63])).
					PodSets(assignments...).
					Obj())
			var ownerFields []string
			for i := range tc.membership {
				uid := fmt.Sprintf("%08x-0000-0000-0000-000000000000", i)
				wl.OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "pod-%d", i)))[:63], uid)
				ownerFields = append(ownerFields, fmt.Sprintf(`"k:{\"uid\":\"%s\"}":{}`, uid))
			}
			wl.ManagedFields(metav1.ManagedFieldsEntry{
				Manager:    "kueue",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: kueue.SchemeGroupVersion.String(),
				FieldsType: "FieldsV1",
				FieldsV1:   metav1.NewFieldsV1(`{"f:metadata":{"f:ownerReferences":{` + strings.Join(ownerFields, ",") + `}}}`),
			})
			var conditions []metav1.Condition
			for i := range 8 {
				conditions = append(conditions, metav1.Condition{
					Type:    fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "type-%d", i)))[:63],
					Status:  metav1.ConditionTrue,
					Reason:  fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "reason-%d", i)))[:63],
					Message: strings.Repeat("m", 1024),
				})
			}
			wl.Conditions(conditions...)

			b, err := json.Marshal(wl.Obj())
			if err != nil {
				t.Fatalf("Failed to marshal the Workload: %v", err)
			}
			projected := projectedWorkloadBytes(templates, int64(tc.membership), tc.withTAS)
			t.Logf("membership=%d, Workload size=%d, projected size=%d", tc.membership, len(b), projected)
			if int64(len(b)) > projected {
				t.Errorf("Workload size %d is above the projected size %d", len(b), projected)
			}
			if projected > safeWorkloadBytes {
				t.Errorf("Projected size %d is above safeWorkloadBytes %d", projected, safeWorkloadBytes)
			}
		})
	}
}
