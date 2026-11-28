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

package equality

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1defaults "k8s.io/kubernetes/pkg/apis/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// withResourceClaims sets PodSpec.ResourceClaims directly, bypassing the
// PodSetWrapper.ResourceClaim helper which also links a matching container
// claim reference.
func withResourceClaims(ps *kueue.PodSet, claims ...corev1.PodResourceClaim) kueue.PodSet {
	ps.Template.Spec.ResourceClaims = claims
	return *ps
}

func TestComparePodTemplate(t *testing.T) {
	base := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "c",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			},
		}},
		NodeSelector: map[string]string{"f1l1": "v1"},
	}
	basePodSet := utiltestingapi.MakePodSet(kueue.NewPodSetReference("pod"), 1).PodSpec(base)

	cases := map[string]struct {
		a       corev1.PodSpec
		b       corev1.PodSpec
		options []ComparePodSetsOption
		want    bool
	}{
		"identical": {
			a:    *base.DeepCopy(),
			b:    *base.DeepCopy(),
			want: true,
		},
		"identical ignoring non-compared PodSpec fields": {
			// Only containers/initContainers/tolerations are compared. nodeSelector
			// and API-defaulted fields are ignored.
			a: func() corev1.PodSpec {
				ps := *base.DeepCopy()
				ps.NodeSelector = map[string]string{"f2l1": "v2"}
				ps.RestartPolicy = corev1.RestartPolicyAlways
				ps.DNSPolicy = corev1.DNSClusterFirst
				ps.SchedulerName = "default-scheduler"
				ps.TerminationGracePeriodSeconds = new(int64(30))
				ps.SecurityContext = &corev1.PodSecurityContext{}
				return ps
			}(),
			b:    *base.DeepCopy(),
			want: true,
		},
		"different container resources": {
			a: func() corev1.PodSpec {
				ps := *base.DeepCopy()
				ps.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
				return ps
			}(),
			b:    *base.DeepCopy(),
			want: false,
		},
		"different tolerations": {
			a: func() corev1.PodSpec {
				ps := *base.DeepCopy()
				ps.Tolerations = []corev1.Toleration{{
					Key:      "k",
					Value:    "v",
					Operator: corev1.TolerationOpEqual,
					Effect:   corev1.TaintEffectNoSchedule,
				}}
				return ps
			}(),
			b:    *base.DeepCopy(),
			want: false,
		},
		"different runtime classes": {
			a:    basePodSet.Clone().RuntimeClass("kata").Obj().Template.Spec,
			b:    basePodSet.Clone().RuntimeClass("runc").Obj().Template.Spec,
			want: false,
		},
		"runtime class on one side": {
			a:    basePodSet.Clone().RuntimeClass("kata").Obj().Template.Spec,
			b:    *base.DeepCopy(),
			want: false,
		},
		"runtime class mismatch ignored for a running workload": {
			a:       basePodSet.Clone().RuntimeClass("kata").Obj().Template.Spec,
			b:       *base.DeepCopy(),
			options: []ComparePodSetsOption{WithIgnoreRuntimeClassName()},
			want:    true,
		},
		"same runtime class": {
			a:    basePodSet.Clone().RuntimeClass("kata").Obj().Template.Spec,
			b:    basePodSet.Clone().RuntimeClass("kata").Obj().Template.Spec,
			want: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ComparePodTemplate(&tc.a, &tc.b, tc.options...); got != tc.want {
				t.Errorf("ComparePodTemplate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompareProvisioningPodTemplates(t *testing.T) {
	baseTemplate := func() *utiltesting.PodTemplateWrapper {
		return utiltesting.MakePodTemplate("pt", "ns").
			Containers(corev1.Container{
				Name:  "c",
				Image: "example.com/app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				},
			}).
			NodeSelector("pool", "gpu").
			RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      "zone",
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"us-east-1a"},
				}},
			}})
	}
	defaulted := func(pt *corev1.PodTemplate) *corev1.PodTemplate {
		out := pt.DeepCopy()
		corev1defaults.SetObjectDefaults_PodTemplate(out)
		return out
	}

	cases := map[string]struct {
		existing *corev1.PodTemplate
		desired  *corev1.PodTemplate
		want     bool
	}{
		"identical": {
			existing: baseTemplate().Obj(),
			desired:  baseTemplate().Obj(),
			want:     true,
		},
		"existing has API defaults": {
			existing: defaulted(baseTemplate().Obj()),
			desired:  baseTemplate().Obj(),
			want:     true,
		},
		"explicit ImagePullPolicy differs from API default": {
			existing: func() *corev1.PodTemplate {
				pt := defaulted(baseTemplate().Obj())
				pt.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullNever
				return pt
			}(),
			desired: baseTemplate().Obj(),
			want:    false,
		},
		"different container requests": {
			existing: defaulted(baseTemplate().Obj()),
			desired: baseTemplate().Containers(corev1.Container{
				Name:  "c",
				Image: "example.com/app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
				},
			}).Obj(),
			want: false,
		},
		"different NodeSelector": {
			existing: defaulted(baseTemplate().Obj()),
			desired:  baseTemplate().NodeSelector("pool", "cpu").Obj(),
			want:     false,
		},
		"different Affinity": {
			existing: defaulted(baseTemplate().Obj()),
			desired: func() *corev1.PodTemplate {
				pt := baseTemplate().Obj()
				pt.Template.Spec.Affinity = nil
				return pt
			}(),
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			existingOrig := tc.existing.DeepCopy()
			desiredOrig := tc.desired.DeepCopy()
			if got := CompareProvisioningPodTemplates(tc.existing, tc.desired); got != tc.want {
				t.Errorf("CompareProvisioningPodTemplates() = %v, want %v", got, tc.want)
			}
			if diff := cmp.Diff(existingOrig, tc.existing); diff != "" {
				t.Errorf("CompareProvisioningPodTemplates() mutated existing (-want/+got):\n%s", diff)
			}
			if diff := cmp.Diff(desiredOrig, tc.desired); diff != "" {
				t.Errorf("CompareProvisioningPodTemplates() mutated desired (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestComparePodSetSlices(t *testing.T) {
	cases := map[string]struct {
		a                         []kueue.PodSet
		b                         []kueue.PodSet
		ignoreTolerations         bool
		ignoreTopologyRequest     bool
		ignoreTopologyIndexLabels bool
		ignoreContainerImages     bool
		wantEquivalent            bool
	}{
		"different name": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps2", 10).SetMinimumCount(5).Obj()},
			wantEquivalent: true,
		},
		"different min count": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(2).Obj()},
			wantEquivalent: false,
		},
		"different node selector": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).NodeSelector(map[string]string{"key": "val"}).Obj()},
			wantEquivalent: true,
		},
		"same required topology": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			wantEquivalent: true,
		},
		"different required topology": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelTopologyZone).Obj()},
			wantEquivalent: false,
		},
		"different required topology ignored": {
			a:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelTopologyZone).Obj()},
			ignoreTopologyRequest: true,
			wantEquivalent:        true,
		},
		"topology request present on one side": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Obj()},
			wantEquivalent: false,
		},
		"required and preferred topology": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).PreferredTopologyRequest(corev1.LabelHostname).Obj()},
			wantEquivalent: false,
		},
		"derived pod index without topology constraint": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).PodIndexLabel(new("index")).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Obj()},
			wantEquivalent: true,
		},
		"different pod index under topology constraint": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index-a")).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index-b")).
				Obj()},
			wantEquivalent: false,
		},
		"legacy and unified slice topology constraints": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				SliceRequiredTopologyRequest(corev1.LabelHostname).
				SliceSizeTopologyRequest(2).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				SliceRequiredTopologyConstraints(kueue.PodsetSliceRequiredTopologyConstraint{
					Topology: corev1.LabelHostname,
					Size:     2,
				}).
				Obj()},
			wantEquivalent: true,
		},
		"different requests": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Request("res", "1").Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Request("res", "2").Obj()},
			wantEquivalent: false,
		},
		"different requests in init containers": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).InitContainers(corev1.Container{
				Image: "img1",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"res": resource.MustParse("1"),
					},
				},
			}).Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).InitContainers(corev1.Container{
				Image: "img1",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"res": resource.MustParse("2"),
					},
				},
			}).Obj()},
			wantEquivalent: false,
		},
		"different requests in toleration": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Toleration(corev1.Toleration{
				Key:      "instance",
				Operator: corev1.TolerationOpEqual,
				Value:    "spot",
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Toleration(corev1.Toleration{
				Key:      "instance",
				Operator: corev1.TolerationOpEqual,
				Value:    "demand",
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj()},
			wantEquivalent: false,
		},
		"different pod-level resources": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).PodLevelRequest("res", "1").Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).PodLevelRequest("res", "2").Obj()},
			wantEquivalent: false,
		},
		"pod-level resources present vs omitted": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).PodLevelRequest("res", "1").Obj()},
			wantEquivalent: false,
		},
		"resource claims present vs omitted": {
			// Set PodSpec.ResourceClaims directly, without a matching container
			// claim reference, so the case exercises only the PodSpec-level
			// field and doesn't also trip the (already covered) Containers comparison.
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b: []kueue.PodSet{withResourceClaims(
				utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj(),
				corev1.PodResourceClaim{Name: "claim", ResourceClaimName: new("rc")},
			)},
			wantEquivalent: false,
		},
		"resource claims in reversed order": {
			// Order is not semantically significant for a +listType=map field, so
			// two PodSets differing only in ResourceClaims order must still be
			// treated as equivalent.
			a: []kueue.PodSet{withResourceClaims(
				utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj(),
				corev1.PodResourceClaim{Name: "claim-a", ResourceClaimName: new("rc-a")},
				corev1.PodResourceClaim{Name: "claim-b", ResourceClaimName: new("rc-b")},
			)},
			b: []kueue.PodSet{withResourceClaims(
				utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj(),
				corev1.PodResourceClaim{Name: "claim-b", ResourceClaimName: new("rc-b")},
				corev1.PodResourceClaim{Name: "claim-a", ResourceClaimName: new("rc-a")},
			)},
			wantEquivalent: true,
		},
		"different count": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 20).SetMinimumCount(5).Obj()},
			wantEquivalent: false,
		},
		"different slice len": {
			a:              []kueue.PodSet{{}, {}},
			b:              []kueue.PodSet{{}, {}, {}},
			wantEquivalent: false,
		},
		"different requests in toleration, ignore tolerations": {
			a: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "spot",
						Effect:   corev1.TaintEffectNoSchedule,
					}).Obj(),
			},
			b: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "demand",
						Effect:   corev1.TaintEffectNoSchedule,
					}).Obj(),
			},
			ignoreTolerations: true,
			wantEquivalent:    true,
		},
		"different requests in node selector": {
			a: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					NodeSelector(map[string]string{"key": "val"}).
					Obj(),
			},
			b: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					NodeSelector(map[string]string{"key": "val2"}).
					Obj(),
			},
			wantEquivalent: true,
		},
		"ignore topology index labels: missing pod index label, subgroup index label, and subgroup count": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index")).
				SubGroupIndexLabel(new("subgroup")).
				SubGroupCount(new(int32(2))).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				Obj()},
			ignoreTopologyIndexLabels: true,
			wantEquivalent:            true,
		},
		"ignore topology index labels disabled: different pod index label": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index")).
				SubGroupCount(new(int32(2))).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				Obj()},
			ignoreTopologyIndexLabels: false,
			wantEquivalent:            false,
		},
		"ignore topology index labels: different constraints should still mismatch": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index")).
				SubGroupCount(new(int32(2))).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelTopologyZone).
				Obj()},
			ignoreTopologyIndexLabels: true,
			wantEquivalent:            false,
		},
		"ignore topology index labels: symmetric comparison": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index")).
				SubGroupIndexLabel(new("subgroup")).
				SubGroupCount(new(int32(2))).
				Obj()},
			ignoreTopologyIndexLabels: true,
			wantEquivalent:            true,
		},
		"different container images": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Image("img1").Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Image("img2").Obj()},
			wantEquivalent: false,
		},
		"different container images, ignore container images": {
			a:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Image("img1").Obj()},
			b:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Image("img2").Obj()},
			ignoreContainerImages: true,
			wantEquivalent:        true,
		},
		"different init container images, ignore container images": {
			a:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).InitContainers(corev1.Container{Name: "init", Image: "img1"}).Obj()},
			b:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).InitContainers(corev1.Container{Name: "init", Image: "img2"}).Obj()},
			ignoreContainerImages: true,
			wantEquivalent:        true,
		},
		"different requests, ignore container images": {
			a:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Image("img1").Request("res", "1").Obj()},
			b:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Image("img2").Request("res", "2").Obj()},
			ignoreContainerImages: true,
			wantEquivalent:        false,
		},
		"different container count, ignore container images": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Containers(
				corev1.Container{Name: "c1", Image: "img"},
				corev1.Container{Name: "c2", Image: "img"},
			).Obj()},
			ignoreContainerImages: true,
			wantEquivalent:        false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			options := make([]ComparePodSetsOption, 0, 1)
			if tc.ignoreTolerations {
				options = append(options, WithIgnoreTolerations())
			}
			if tc.ignoreTopologyRequest {
				options = append(options, WithIgnoreTopologyRequest())
			}
			if tc.ignoreTopologyIndexLabels {
				options = append(options, WithIgnoreTopologyIndexLabels())
			}
			if tc.ignoreContainerImages {
				options = append(options, WithIgnoreContainerImages())
			}
			got := ComparePodSetSlices(tc.a, tc.b, options...)
			if got != tc.wantEquivalent {
				t.Errorf("Unexpected result, want %v", tc.wantEquivalent)
			}
		})
	}
}

func TestComparePodSetsIgnoringScale(t *testing.T) {
	cases := map[string]struct {
		a    []kueue.PodSet
		b    []kueue.PodSet
		want bool
	}{
		"same template, different count": {
			a:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "500m").Obj()},
			b:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 3).Request(corev1.ResourceCPU, "500m").Obj()},
			want: true,
		},
		"same template, different minCount": {
			a:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 3).Request(corev1.ResourceCPU, "500m").SetMinimumCount(1).Obj()},
			b:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 3).Request(corev1.ResourceCPU, "500m").SetMinimumCount(3).Obj()},
			want: true,
		},
		"different cpu request, same count": {
			a:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 3).Request(corev1.ResourceCPU, "500m").Obj()},
			b:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 3).Request(corev1.ResourceCPU, "1").Obj()},
			want: false,
		},
		"different cpu request and count": {
			a:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "500m").Obj()},
			b:    []kueue.PodSet{*utiltestingapi.MakePodSet("main", 3).Request(corev1.ResourceCPU, "1").Obj()},
			want: false,
		},
		"matched by name when the order differs": {
			a: []kueue.PodSet{
				*utiltestingapi.MakePodSet("executor", 3).Request(corev1.ResourceCPU, "500m").Obj(),
				*utiltestingapi.MakePodSet("driver", 1).Request(corev1.ResourceCPU, "500m").Obj(),
			},
			b: []kueue.PodSet{
				*utiltestingapi.MakePodSet("driver", 1).Request(corev1.ResourceCPU, "500m").Obj(),
				*utiltestingapi.MakePodSet("executor", 2).Request(corev1.ResourceCPU, "500m").Obj(),
			},
			want: true,
		},
		"different pod set name": {
			a:    []kueue.PodSet{*utiltestingapi.MakePodSet("executor", 1).Request(corev1.ResourceCPU, "500m").Obj()},
			b:    []kueue.PodSet{*utiltestingapi.MakePodSet("driver", 1).Request(corev1.ResourceCPU, "500m").Obj()},
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ComparePodSetsIgnoringScale(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("ComparePodSetsIgnoringScale() = %t, want %t", got, tc.want)
			}
		})
	}
}
