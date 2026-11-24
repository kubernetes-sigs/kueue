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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

const (
	sevenDaysInSeconds int32 = 7 * 24 * 60 * 60

	windowSecondsMinErrorMessage = "should be greater than or equal to 1"
	windowSecondsMaxErrorMessage = "should be less than or equal to 604800"
)

var _ = ginkgo.Describe("PreemptionLimit Validation", func() {
	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup)
	})

	ginkgo.When("Creating PreemptionLimit", func() {
		ginkgo.DescribeTable("Validate limit",
			func(limit int32, wantErrMessage string) {
				pl := utiltestingalpha.MakePreemptionLimit("pl-limit", kueuealpha.GlobalLimitAggregation).
					Limit(limit).
					WindowSeconds(60).
					Obj()
				ginkgo.DeferCleanup(func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pl, true)
				})

				err := k8sClient.Create(ctx, pl)
				if wantErrMessage != "" {
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring(wantErrMessage))
					return
				}
				gomega.Expect(err).To(gomega.Succeed())
			},
			ginkgo.Entry("Allow zero limit", int32(0), ""),
			ginkgo.Entry("Allow positive limit", int32(10), ""),
			ginkgo.Entry("Disallow negative limit", int32(-1), negativeValueErrorMessage),
		)

		ginkgo.DescribeTable("Validate windowSeconds",
			func(windowSeconds int32, wantErrMessage string) {
				pl := utiltestingalpha.MakePreemptionLimit("pl-window-seconds", kueuealpha.GlobalLimitAggregation).
					Limit(5).
					WindowSeconds(windowSeconds).
					Obj()
				ginkgo.DeferCleanup(func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pl, true)
				})

				err := k8sClient.Create(ctx, pl)
				if wantErrMessage != "" {
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring(wantErrMessage))
					return
				}
				gomega.Expect(err).To(gomega.Succeed())
			},
			ginkgo.Entry("Allow 1 second window", int32(1), ""),
			ginkgo.Entry("Allow 7 days window", sevenDaysInSeconds, ""),
			ginkgo.Entry("Disallow zero windowSeconds (omitted)", int32(0), "spec.windowSeconds: Required value"),
			ginkgo.Entry("Disallow negative windowSeconds", int32(-1), windowSecondsMinErrorMessage),
			ginkgo.Entry("Disallow windowSeconds larger than 7 days", sevenDaysInSeconds+1, windowSecondsMaxErrorMessage),
		)

		ginkgo.It("Should disallow explicit zero windowSeconds", func() {
			pl := &unstructured.Unstructured{}
			pl.SetAPIVersion(kueuealpha.SchemeGroupVersion.String())
			pl.SetKind("PreemptionLimit")
			pl.SetName("pl-explicit-zero-window")
			pl.Object["spec"] = map[string]any{
				"aggregation":    string(kueuealpha.GlobalLimitAggregation),
				"configSelector": map[string]any{},
				"limit":          int64(5),
				"windowSeconds":  int64(0),
			}
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, utiltestingalpha.MakePreemptionLimit(pl.GetName(), "").Obj(), true)
			})

			err := k8sClient.Create(ctx, pl)
			gomega.Expect(err).To(utiltesting.BeInvalidError())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring(windowSecondsMinErrorMessage))
		})

		ginkgo.DescribeTable("Validate aggregation",
			func(aggregation kueuealpha.PreemptionLimitAggregation, wantErrMessage string) {
				pl := utiltestingalpha.MakePreemptionLimit("pl-aggregation", aggregation).
					Limit(5).
					WindowSeconds(60).
					Obj()
				ginkgo.DeferCleanup(func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pl, true)
				})

				err := k8sClient.Create(ctx, pl)
				if wantErrMessage != "" {
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring(wantErrMessage))
					return
				}
				gomega.Expect(err).To(gomega.Succeed())
			},
			ginkgo.Entry("Allow Global aggregation", kueuealpha.GlobalLimitAggregation, ""),
			ginkgo.Entry("Allow ByPreemptorClusterQueue aggregation", kueuealpha.ByPreemptorCQLimitAggregation, ""),
			ginkgo.Entry("Allow ByPreempteeClusterQueue aggregation", kueuealpha.ByPreempteeCQLimitAggregation, ""),
			ginkgo.Entry("Allow ByPreempteeWorkload aggregation", kueuealpha.ByPreempteeWorkloadLimitAggregation, ""),
			ginkgo.Entry("Disallow empty aggregation (omitted)", kueuealpha.PreemptionLimitAggregation(""), "spec.aggregation: Required value"),
			ginkgo.Entry("Disallow unknown aggregation", kueuealpha.PreemptionLimitAggregation("InvalidAggregation"), "Unsupported value"),
		)

		ginkgo.It("Should disallow explicit empty aggregation", func() {
			pl := &unstructured.Unstructured{}
			pl.SetAPIVersion(kueuealpha.SchemeGroupVersion.String())
			pl.SetKind("PreemptionLimit")
			pl.SetName("pl-explicit-empty-aggregation")
			pl.Object["spec"] = map[string]any{
				"aggregation":    "",
				"configSelector": map[string]any{},
				"limit":          int64(5),
				"windowSeconds":  int64(60),
			}
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, utiltestingalpha.MakePreemptionLimit(pl.GetName(), "").Obj(), true)
			})

			err := k8sClient.Create(ctx, pl)
			gomega.Expect(err).To(utiltesting.BeInvalidError())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("Unsupported value"))
		})

		ginkgo.It("Should disallow omitted aggregation", func() {
			pl := &unstructured.Unstructured{}
			pl.SetAPIVersion(kueuealpha.SchemeGroupVersion.String())
			pl.SetKind("PreemptionLimit")
			pl.SetName("pl-omitted-aggregation")
			pl.Object["spec"] = map[string]any{
				"configSelector": map[string]any{},
				"limit":          int64(5),
				"windowSeconds":  int64(60),
			}
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, utiltestingalpha.MakePreemptionLimit(pl.GetName(), "").Obj(), true)
			})

			err := k8sClient.Create(ctx, pl)
			gomega.Expect(err).To(utiltesting.BeInvalidError())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("spec.aggregation: Required value"))
		})
	})

	ginkgo.When("Updating PreemptionLimit", func() {
		ginkgo.It("Should validate limit, windowSeconds, and aggregation on update", func() {
			pl := utiltestingalpha.MakePreemptionLimit("pl-update", kueuealpha.GlobalLimitAggregation).
				Limit(5).
				WindowSeconds(60).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, pl)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pl, true)
			})

			ginkgo.By("Allowing an update that sets limit to zero", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.Limit = 0
				gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())
			})

			ginkgo.By("Rejecting an update that sets negative limit", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.Limit = -1
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(negativeValueErrorMessage))
			})

			ginkgo.By("Allowing an update that sets windowSeconds to 1 second", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.WindowSeconds = 1
				gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())
			})

			ginkgo.By("Allowing an update that sets windowSeconds to 7 days", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.WindowSeconds = sevenDaysInSeconds
				gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())
			})

			ginkgo.By("Rejecting an update that sets windowSeconds to zero", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.WindowSeconds = 0
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring("spec.windowSeconds: Required value"))
			})

			ginkgo.By("Rejecting an update that sets negative windowSeconds", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.WindowSeconds = -1
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(windowSecondsMinErrorMessage))
			})

			ginkgo.By("Rejecting an update that sets windowSeconds larger than 7 days", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.WindowSeconds = sevenDaysInSeconds + 1
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(windowSecondsMaxErrorMessage))
			})

			ginkgo.By("Allowing an update that changes aggregation to another valid value", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.Aggregation = kueuealpha.ByPreempteeWorkloadLimitAggregation
				gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())
			})

			ginkgo.By("Rejecting an update that sets an invalid aggregation", func() {
				var fetched kueuealpha.PreemptionLimit
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pl), &fetched)).To(gomega.Succeed())
				fetched.Spec.Aggregation = kueuealpha.PreemptionLimitAggregation("InvalidAggregation")
				err := k8sClient.Update(ctx, &fetched)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring("Unsupported value"))
			})
		})
	})
})
