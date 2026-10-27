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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

const (
	minMaxValueErrorMessage               = "minValue must be less than or equal to maxValue"
	negativeValueErrorMessage             = "should be greater than or equal to 0"
	podPriorityClassKindErrorMessage      = "only the PriorityClass kind is allowed for the scheduling.k8s.io group"
	workloadPriorityClassKindErrorMessage = "only the WorkloadPriorityClass kind is allowed for the kueue.x-k8s.io group"
)

func makePreemptionConfigWithNumericLabel(name string, minValue, maxValue *int32) *kueuealpha.PreemptionConfig {
	return &kueuealpha.PreemptionConfig{
		Name: name,
		Spec: kueuealpha.PreemptionConfigSpec{
			Rules: []kueuealpha.PreemptionConfigPreemptionRule{{
				Name: "rule",
				ActivationPolicy: kueuealpha.PreemptionConfigActivationPolicy{
					Trigger: kueuealpha.Always,
				},
				CandidateSelectors: []kueuealpha.PreemptionConfigPreemptionCandidateSelector{{
					Scope: kueuealpha.WithinClusterQueue,
					NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{{
						Key:      "example.com/number-of-tpus",
						MinValue: minValue,
						MaxValue: maxValue,
					}},
				}},
			}},
		},
	}
}

var _ = ginkgo.Describe("PreemptionConfig Validation", func() {
	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})
	ginkgo.AfterEach(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("Creating PreemptionConfig", func() {
		ginkgo.DescribeTable("Validate numericLabels minValue and maxValue",
			func(minValue, maxValue *int32, wantErrMessage string) {
				pc := makePreemptionConfigWithNumericLabel("pc-numeric-label", minValue, maxValue)

				err := k8sClient.Create(ctx, pc)
				if wantErrMessage != "" {
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring(wantErrMessage))
					return
				}
				gomega.Expect(err).To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pc, true)
			},
			ginkgo.Entry("Allow neither minValue nor maxValue", nil, nil, ""),
			ginkgo.Entry("Allow only minValue", ptr.To[int32](5), nil, ""),
			ginkgo.Entry("Allow only maxValue", nil, ptr.To[int32](5), ""),
			ginkgo.Entry("Allow minValue less than maxValue", ptr.To[int32](1), ptr.To[int32](10), ""),
			ginkgo.Entry("Allow minValue equal to maxValue", ptr.To[int32](4), ptr.To[int32](4), ""),
			ginkgo.Entry("Allow minValue and maxValue both zero", ptr.To[int32](0), ptr.To[int32](0), ""),
			ginkgo.Entry("Disallow minValue greater than maxValue", ptr.To[int32](10), ptr.To[int32](1), minMaxValueErrorMessage),
			ginkgo.Entry("Disallow minValue greater than maxValue by one", ptr.To[int32](5), ptr.To[int32](4), minMaxValueErrorMessage),
			ginkgo.Entry("Disallow negative minValue", ptr.To[int32](-1), nil, negativeValueErrorMessage),
			ginkgo.Entry("Disallow negative maxValue", nil, ptr.To[int32](-1), negativeValueErrorMessage),
			ginkgo.Entry("Disallow negative minValue with non-negative maxValue", ptr.To[int32](-1), ptr.To[int32](5), negativeValueErrorMessage),
			ginkgo.Entry("Disallow negative minValue and maxValue", ptr.To[int32](-5), ptr.To[int32](-1), negativeValueErrorMessage),
		)

		ginkgo.DescribeTable("Validate priority constraint and priority class selectors",
			func(
				preemptorSelector *kueuealpha.PreemptionConfigPriorityClassSelector,
				priority *kueuealpha.PreemptionConfigPriorityConstraint,
				candidateSelector *kueuealpha.PreemptionConfigPriorityClassSelector,
				wantErrMessage string,
			) {
				pc := &kueuealpha.PreemptionConfig{
					Name: "pc-priority-validation",
					Spec: kueuealpha.PreemptionConfigSpec{
						Rules: []kueuealpha.PreemptionConfigPreemptionRule{{
							Name:                           "rule",
							PreemptorPriorityClassSelector: preemptorSelector,
							ActivationPolicy: kueuealpha.PreemptionConfigActivationPolicy{
								Trigger: kueuealpha.Always,
							},
							CandidateSelectors: []kueuealpha.PreemptionConfigPreemptionCandidateSelector{{
								Scope:                 kueuealpha.WithinClusterQueue,
								Priority:              priority,
								PriorityClassSelector: candidateSelector,
							}},
						}},
					},
				}

				err := k8sClient.Create(ctx, pc)
				if wantErrMessage != "" {
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring(wantErrMessage))
					return
				}
				gomega.Expect(err).To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pc, true)
			},
			ginkgo.Entry("Allow preemptorPriorityClassSelector with WorkloadPriorityClass sourceRef and In/NotIn nameExpressions",
				&kueuealpha.PreemptionConfigPriorityClassSelector{
					SourceRef: &kueuealpha.PriorityClassSourceRef{
						Group: kueuealpha.WorkloadPriorityClassGroup,
						Kind:  kueuealpha.WorkloadPriorityClassKind,
					},
					NameExpressions: []kueuealpha.PriorityClassSelectorRequirement{
						{
							Operator: kueuealpha.PriorityClassSelectorOpIn,
							Values:   []string{"high-priority", "critical-priority"},
						},
						{
							Operator: kueuealpha.PriorityClassSelectorOpNotIn,
							Values:   []string{"mid-priority"},
						},
					},
				},
				nil,
				nil,
				"",
			),
			ginkgo.Entry("Allow candidate priorityClassSelector with PriorityClass sourceRef and In/NotIn nameExpressions",
				nil,
				nil,
				&kueuealpha.PreemptionConfigPriorityClassSelector{
					SourceRef: &kueuealpha.PriorityClassSourceRef{
						Group: kueuealpha.PodPriorityClassGroup,
						Kind:  kueuealpha.PodPriorityClassKind,
					},
					NameExpressions: []kueuealpha.PriorityClassSelectorRequirement{
						{
							Operator: kueuealpha.PriorityClassSelectorOpIn,
							Values:   []string{"low-priority", "default"},
						},
						{
							Operator: kueuealpha.PriorityClassSelectorOpNotIn,
							Values:   []string{"high-priority"},
						},
					},
				},
				"",
			),
			ginkgo.Entry("Allow candidate priority constraint combined with priorityClassSelector",
				nil,
				&kueuealpha.PreemptionConfigPriorityConstraint{
					Mode:       kueuealpha.Base,
					Comparison: kueuealpha.LessThan,
				},
				&kueuealpha.PreemptionConfigPriorityClassSelector{
					SourceRef: &kueuealpha.PriorityClassSourceRef{
						Group: kueuealpha.WorkloadPriorityClassGroup,
						Kind:  kueuealpha.WorkloadPriorityClassKind,
					},
					NameExpressions: []kueuealpha.PriorityClassSelectorRequirement{
						{
							Operator: kueuealpha.PriorityClassSelectorOpIn,
							Values:   []string{"low-priority"},
						},
					},
				},
				"",
			),
			ginkgo.Entry("Disallow sourceRef with group scheduling.k8s.io and kind WorkloadPriorityClass",
				nil,
				nil,
				&kueuealpha.PreemptionConfigPriorityClassSelector{
					SourceRef: &kueuealpha.PriorityClassSourceRef{
						Group: kueuealpha.PodPriorityClassGroup,
						Kind:  kueuealpha.WorkloadPriorityClassKind,
					},
				},
				podPriorityClassKindErrorMessage,
			),
			ginkgo.Entry("Disallow sourceRef with group kueue.x-k8s.io and kind PriorityClass",
				nil,
				nil,
				&kueuealpha.PreemptionConfigPriorityClassSelector{
					SourceRef: &kueuealpha.PriorityClassSourceRef{
						Group: kueuealpha.WorkloadPriorityClassGroup,
						Kind:  kueuealpha.PodPriorityClassKind,
					},
				},
				workloadPriorityClassKindErrorMessage,
			),
			ginkgo.Entry("Disallow invalid DNS-subdomain name in nameExpressions[0].values[0]",
				nil,
				nil,
				&kueuealpha.PreemptionConfigPriorityClassSelector{
					NameExpressions: []kueuealpha.PriorityClassSelectorRequirement{
						{
							Operator: kueuealpha.PriorityClassSelectorOpIn,
							Values:   []string{"Invalid_Name"},
						},
					},
				},
				"spec.rules[0].candidateSelectors[0].priorityClassSelector.nameExpressions[0].values[0]",
			),
			ginkgo.Entry("Disallow empty values in nameExpressions[0]",
				nil,
				nil,
				&kueuealpha.PreemptionConfigPriorityClassSelector{
					NameExpressions: []kueuealpha.PriorityClassSelectorRequirement{
						{
							Operator: kueuealpha.PriorityClassSelectorOpIn,
							Values:   []string{},
						},
					},
				},
				"spec.rules[0].candidateSelectors[0].priorityClassSelector.nameExpressions[0].values",
			),
			ginkgo.Entry("Disallow priority.mode without priority.comparison",
				nil,
				&kueuealpha.PreemptionConfigPriorityConstraint{
					Mode: kueuealpha.Base,
				},
				nil,
				"spec.rules[0].candidateSelectors[0].priority.comparison",
			),
			ginkgo.Entry("Disallow priority.comparison without priority.mode",
				nil,
				&kueuealpha.PreemptionConfigPriorityConstraint{
					Comparison: kueuealpha.LessThan,
				},
				nil,
				"spec.rules[0].candidateSelectors[0].priority.mode",
			),
		)
	})

	ginkgo.When("Updating PreemptionConfig", func() {
		ginkgo.It("Should validate numericLabels minValue and maxValue on update", func() {
			const maxValue int32 = 10
			pc := makePreemptionConfigWithNumericLabel("pc-numeric-label-update", ptr.To[int32](1), ptr.To(maxValue))
			behavioral.MustCreate(ctx, k8sClient, pc)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pc, true)
			})

			ginkgo.By("Rejecting an update that sets minValue greater than maxValue", func() {
				var fetched kueuealpha.PreemptionConfig
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pc), &fetched)).To(gomega.Succeed())
				fetched.Spec.Rules[0].CandidateSelectors[0].NumericLabels[0].MinValue = ptr.To(maxValue + 1)
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(minMaxValueErrorMessage))
			})

			ginkgo.By("Allowing an update that sets minValue equal to maxValue", func() {
				var fetched kueuealpha.PreemptionConfig
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pc), &fetched)).To(gomega.Succeed())
				fetched.Spec.Rules[0].CandidateSelectors[0].NumericLabels[0].MinValue = ptr.To(maxValue)
				gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())
			})

			ginkgo.By("Allowing an update that removes maxValue", func() {
				var fetched kueuealpha.PreemptionConfig
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pc), &fetched)).To(gomega.Succeed())
				fetched.Spec.Rules[0].CandidateSelectors[0].NumericLabels[0].MinValue = ptr.To(maxValue + 1)
				fetched.Spec.Rules[0].CandidateSelectors[0].NumericLabels[0].MaxValue = nil
				gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())
			})

			ginkgo.By("Rejecting an update that sets negative minValue", func() {
				var fetched kueuealpha.PreemptionConfig
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pc), &fetched)).To(gomega.Succeed())
				fetched.Spec.Rules[0].CandidateSelectors[0].NumericLabels[0].MinValue = ptr.To[int32](-1)
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(negativeValueErrorMessage))
			})

			ginkgo.By("Rejecting an update that sets negative maxValue", func() {
				var fetched kueuealpha.PreemptionConfig
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pc), &fetched)).To(gomega.Succeed())
				fetched.Spec.Rules[0].CandidateSelectors[0].NumericLabels[0].MinValue = nil
				fetched.Spec.Rules[0].CandidateSelectors[0].NumericLabels[0].MaxValue = ptr.To[int32](-1)
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(negativeValueErrorMessage))
			})
		})
	})
})
