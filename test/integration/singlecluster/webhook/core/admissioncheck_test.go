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
	"context"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AdmissionCheck Webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.When("Creating a AdmissionCheck", func() {
		ginkgo.DescribeTable("Defaulting on creating", func(ac, wantAC kueue.AdmissionCheck) {
			util.MustCreate(ctx, k8sClient, &ac)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &ac, true)
			}()
			gomega.Expect(ac).To(gomega.BeComparableTo(wantAC,
				cmpopts.IgnoreTypes(kueue.AdmissionCheckStatus{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields")))
		},
			ginkgo.Entry("All defaults",
				kueue.AdmissionCheck{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "ac-controller",
					},
				},
				kueue.AdmissionCheck{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.AdmissionCheckSpec{
						ControllerName:    "ac-controller",
						RetryDelayMinutes: ptr.To[int64](15),
					},
				},
			),
		)

		ginkgo.DescribeTable("Validate AdmissionCheck on creation", func(ac kueue.AdmissionCheck, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, &ac)
			if err == nil {
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &ac, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("Should fail to create AdmissionCheck with no controller name",
				kueue.AdmissionCheck{
					Spec: kueue.AdmissionCheckSpec{
						Parameters: &kueue.AdmissionCheckParametersReference{
							APIGroup: "ref.api.group",
							Kind:     "RefKind",
							Name:     "ref-name",
						},
					},
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Should fail to create AdmissionCheck with bad ref api group",
				kueue.AdmissionCheck{
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
						Parameters: &kueue.AdmissionCheckParametersReference{
							APIGroup: "ref.api.group/Bad",
							Kind:     "RefKind",
							Name:     "ref-name",
						},
					},
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Should fail to create AdmissionCheck with no ref api group",
				kueue.AdmissionCheck{
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
						Parameters: &kueue.AdmissionCheckParametersReference{
							Kind: "RefKind",
							Name: "ref-name",
						},
					},
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Should fail to create AdmissionCheck with bad ref kind",
				kueue.AdmissionCheck{
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
						Parameters: &kueue.AdmissionCheckParametersReference{
							APIGroup: "ref.api.group",
							Kind:     "RefKind/Bad",
							Name:     "ref-name",
						},
					},
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Should fail to create AdmissionCheck with no ref kind",
				kueue.AdmissionCheck{
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
						Parameters: &kueue.AdmissionCheckParametersReference{
							APIGroup: "ref.api.group",
							Name:     "ref-name",
						},
					},
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Should fail to create AdmissionCheck with bad ref name",
				kueue.AdmissionCheck{
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
						Parameters: &kueue.AdmissionCheckParametersReference{
							APIGroup: "ref.api.group",
							Kind:     "RefKind",
							Name:     "ref-name/Bad",
						},
					},
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Should fail to create AdmissionCheck with no ref name",
				kueue.AdmissionCheck{
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
						Parameters: &kueue.AdmissionCheckParametersReference{
							APIGroup: "ref.api.group",
							Kind:     "RefKind",
						},
					},
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Should allow to create AdmissionCheck with no parameters",
				kueue.AdmissionCheck{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
					},
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("Should allow to create AdmissionCheck with valid spec",
				kueue.AdmissionCheck{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.AdmissionCheckSpec{
						ControllerName: "controller-name",
						Parameters: &kueue.AdmissionCheckParametersReference{
							APIGroup: "ref.api.group",
							Kind:     "RefKind",
							Name:     "ref-name",
						},
					},
				},
				gomega.Succeed(),
			),
		)
	})

	ginkgo.It("Should allow to update AdmissionCheck when changing parameters", func() {
		ginkgo.By("Creating a new AdmissionCheck")
		ac := testing.MakeAdmissionCheck("admission-check").
			ControllerName("controller-name").
			Parameters("ref.api.group", "RefKind", "ref-name").
			Obj()
		util.MustCreate(ctx, k8sClient, ac)

		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		}()

		gomega.Eventually(func(g gomega.Gomega) {
			var updateAC kueue.AdmissionCheck
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), &updateAC)).Should(gomega.Succeed())
			updateAC.Spec.Parameters.APIGroup = "ref.api.group2"
			updateAC.Spec.Parameters.Kind = "RefKind2"
			updateAC.Spec.Parameters.Name = "ref-name2"
			g.Expect(k8sClient.Update(ctx, &updateAC)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should allow to update AdmissionCheck when removing parameters", func() {
		ginkgo.By("Creating a new AdmissionCheck")
		ac := testing.MakeAdmissionCheck("admission-check").
			ControllerName("controller-name").
			Parameters("ref.api.group", "RefKind", "ref-name").
			Obj()
		util.MustCreate(ctx, k8sClient, ac)

		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		}()

		gomega.Eventually(func(g gomega.Gomega) {
			var updateAC kueue.AdmissionCheck
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), &updateAC)).Should(gomega.Succeed())
			updateAC.Spec.Parameters = nil
			g.Expect(k8sClient.Update(ctx, &updateAC)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should fail to update AdmissionCheck when breaking parameters", func() {
		ginkgo.By("Creating a new AdmissionCheck")
		ac := testing.MakeAdmissionCheck("admission-check").
			ControllerName("controller-name").
			Parameters("ref.api.group", "RefKind", "ref-name").
			Obj()
		util.MustCreate(ctx, k8sClient, ac)

		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		}()

		gomega.Eventually(func(g gomega.Gomega) {
			var updateAC kueue.AdmissionCheck
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), &updateAC)).Should(gomega.Succeed())
			updateAC.Spec.Parameters.Name = ""
			g.Expect(k8sClient.Update(ctx, &updateAC)).Should(testing.BeInvalidError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should fail to update AdmissionCheck when breaking parameters", func() {
		ginkgo.By("Creating a new AdmissionCheck")
		ac := testing.MakeAdmissionCheck("admission-check").
			ControllerName("controller-name").
			Parameters("ref.api.group", "RefKind", "ref-name").
			Obj()
		util.MustCreate(ctx, k8sClient, ac)

		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		}()

		gomega.Eventually(func(g gomega.Gomega) {
			var updateAC kueue.AdmissionCheck
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), &updateAC)).Should(gomega.Succeed())
			updateAC.Spec.ControllerName = "controller-name2"
			g.Expect(k8sClient.Update(ctx, &updateAC)).Should(testing.BeInvalidError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
