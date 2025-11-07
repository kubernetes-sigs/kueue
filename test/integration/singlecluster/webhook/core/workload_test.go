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
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var ns *corev1.Namespace

const (
	workloadName    = "workload-test"
	podSetsMaxItems = 8
)

var _ = ginkgo.Describe("Workload defaulting webhook", ginkgo.Ordered, func() {
	var _ = ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	var _ = ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.Context("When creating a Workload", func() {
		ginkgo.It("Should set default podSet name", func() {
			ginkgo.By("Creating a new Workload")
			// Not using the wrappers to avoid hiding any defaulting.
			workload := kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: ns.Name},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet("", 1).
							Containers(corev1.Container{}).
							Obj(),
					},
				},
			}
			util.MustCreate(ctx, k8sClient, &workload)

			created := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      workload.Name,
				Namespace: workload.Namespace,
			}, created)).Should(gomega.Succeed())

			gomega.Expect(created.Spec.PodSets[0].Name).Should(gomega.Equal(kueue.DefaultPodSetName))
		})

		ginkgo.It("Shouldn't set podSet name if multiple", func() {
			ginkgo.By("Creating a new Workload")
			// Not using the wrappers to avoid hiding any defaulting.
			workload := kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: ns.Name},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet("", 1).
							Containers(corev1.Container{}).
							Obj(),
						*utiltestingapi.MakePodSet("", 1).
							Containers(corev1.Container{}).
							Obj(),
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, &workload)).Should(utiltesting.BeInvalidError())
		})
	})
})

var _ = ginkgo.Describe("Workload validating webhook", ginkgo.Ordered, func() {
	var _ = ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	var _ = ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.Context("When creating a Workload", func() {
		ginkgo.DescribeTable("Should have valid PodSet when creating", func(podSetsCapacity int, podSetCount int, isInvalid bool) {
			podSets := make([]kueue.PodSet, podSetsCapacity)
			for i := range podSets {
				podSets[i] = *utiltestingapi.MakePodSet(kueue.NewPodSetReference(fmt.Sprintf("ps%d", i)), podSetCount).Obj()
			}
			workload := utiltestingapi.MakeWorkload(workloadName, ns.Name).PodSets(podSets...).Obj()
			err := k8sClient.Create(ctx, workload)
			if isInvalid {
				gomega.Expect(err).Should(utiltesting.BeInvalidError())
			} else {
				gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			}
		},
			ginkgo.Entry("podSets count less than 1", 0, 1, true),
			ginkgo.Entry("podSets count more than 8", podSetsMaxItems+1, 1, true),
			ginkgo.Entry("valid podSet, count can be 0", 3, 0, false),
			ginkgo.Entry("valid podSet", 3, 3, false),
		)

		ginkgo.DescribeTable("Should have valid values when creating", func(w func() *kueue.Workload, matcher gomegatypes.GomegaMatcher) {
			gomega.Expect(k8sClient.Create(ctx, w())).Should(matcher)
		},
			ginkgo.Entry("valid workload",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).PodSets(
						*utiltestingapi.MakePodSet("driver", 1).Obj(),
						*utiltestingapi.MakePodSet("workers", 100).Obj(),
					).Obj()
				},
				gomega.Succeed()),
			ginkgo.Entry("invalid podSet name",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).PodSets(
						*utiltestingapi.MakePodSet("@driver", 1).Obj(),
					).Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid priorityClassRef.name",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodPriorityClassRef("invalid_class").
						Priority(0).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid priorityClassRef.group",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PriorityClassRef(&kueue.PriorityClassRef{
							Group: "test",
							Kind:  kueue.PodPriorityClassKind,
							Name:  "low",
						}).
						Priority(100).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid priorityClassRef.kind",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PriorityClassRef(&kueue.PriorityClassRef{
							Group: kueue.PodPriorityClassGroup,
							Kind:  "test",
							Name:  "low",
						}).
						Priority(100).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("incompatible priorityClassRef.kind (scheduling.k8s.io)",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PriorityClassRef(&kueue.PriorityClassRef{
							Group: kueue.PodPriorityClassGroup,
							Kind:  kueue.WorkloadPriorityClassKind,
							Name:  "low",
						}).
						Priority(100).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("incompatible priorityClassRef.kind (kueue.x-k8s.io)",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PriorityClassRef(&kueue.PriorityClassRef{
							Group: kueue.WorkloadPriorityClassGroup,
							Kind:  kueue.PodPriorityClassKind,
							Name:  "low",
						}).
						Priority(100).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("omitted priorityClassRef is valid",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Priority(0).Obj()
				},
				gomega.Succeed()),
			ginkgo.Entry("priority should not be nil when priorityClassRef is set",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodPriorityClassRef("priority").
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid queueName",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Queue("@invalid").
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("should not request num-pods resource",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("bad", 1).
								InitContainers(
									utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
										corev1.ResourcePods: "1",
									})...,
								).
								Containers(
									utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
										corev1.ResourcePods: "1",
									})...,
								).
								Obj(),
						).
						Obj()
				},
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("empty podSetUpdates should be valid since it is optional",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						AdmissionChecks(kueue.AdmissionCheckState{}).
						Obj()
				},
				gomega.Succeed()),
			ginkgo.Entry("matched names in podSetUpdates with names in podSets",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("first", 1).Obj(),
							*utiltestingapi.MakePodSet("second", 1).Obj(),
						).
						AdmissionChecks(
							kueue.AdmissionCheckState{
								PodSetUpdates: []kueue.PodSetUpdate{
									{
										Name:        "first",
										Labels:      map[string]string{"l1": "first"},
										Annotations: map[string]string{"foo": "bar"},
										Tolerations: []corev1.Toleration{
											{
												Key:               "t1",
												Operator:          corev1.TolerationOpEqual,
												Value:             "t1v",
												Effect:            corev1.TaintEffectNoExecute,
												TolerationSeconds: ptr.To[int64](5),
											},
										},
										NodeSelector: map[string]string{"type": "first"},
									},
									{
										Name:        "second",
										Labels:      map[string]string{"l2": "second"},
										Annotations: map[string]string{"foo": "baz"},
										Tolerations: []corev1.Toleration{
											{
												Key:               "t2",
												Operator:          corev1.TolerationOpEqual,
												Value:             "t2v",
												Effect:            corev1.TaintEffectNoExecute,
												TolerationSeconds: ptr.To[int64](10),
											},
										},
										NodeSelector: map[string]string{"type": "second"},
									},
								},
							},
						).
						Obj()
				},
				gomega.Succeed()),
			ginkgo.Entry("invalid podSet minCount (negative)",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("ps1", 3).SetMinimumCount(-1).Obj(),
						).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid podSet minCount (too big)",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("ps1", 3).SetMinimumCount(4).Obj(),
						).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("too many variable count podSets",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("ps1", 3).SetMinimumCount(2).Obj(),
							*utiltestingapi.MakePodSet("ps2", 3).SetMinimumCount(1).Obj(),
						).
						Obj()
				},
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("invalid maximumExexcutionTimeSeconds",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						MaximumExecutionTimeSeconds(0).
						Obj()
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("valid maximumExexcutionTimeSeconds",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						MaximumExecutionTimeSeconds(1).
						Obj()
				},
				gomega.Succeed()),
		)

		ginkgo.DescribeTable("Should have valid values when setting Admission", func(w func() *kueue.Workload, a *kueue.Admission, matcher gomegatypes.GomegaMatcher) {
			wl := w()
			util.MustCreate(ctx, k8sClient, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				err := workload.PatchAdmissionStatus(ctx, k8sClient, wl, util.RealClock, func() (*kueue.Workload, bool, error) {
					return wl, workload.SetQuotaReservation(wl, a, util.RealClock), nil
				})
				g.Expect(err).Should(matcher)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},
			ginkgo.Entry("invalid clusterQueue name",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				utiltestingapi.MakeAdmission("@invalid").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid podSet name in status assignment",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				utiltestingapi.MakeAdmission("cluster-queue", "@invalid").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("mismatched names in admission with names in podSets",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("main2", 1).Obj(),
							*utiltestingapi.MakePodSet("main1", 1).Obj(),
						).
						Obj()
				},
				utiltestingapi.MakeAdmission("cluster-queue", "main1", "main2", "main3").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("assignment usage should be divisible by count",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
								Request(corev1.ResourceCPU, "1").
								Obj(),
						).
						Obj()
				},
				utiltestingapi.MakeAdmission("cluster-queue").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flv", "1").
						Count(3).
						Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Pending delayedTopologyRequest",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
								Request(corev1.ResourceCPU, "1").
								Obj(),
						).
						Obj()
				},
				utiltestingapi.MakeAdmission("cluster-queue").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flv", "3").
						Count(3).
						DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
						Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Ready delayedTopologyRequest",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
								Request(corev1.ResourceCPU, "1").
								Obj(),
						).
						Obj()
				},
				utiltestingapi.MakeAdmission("cluster-queue").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flv", "3").
						Count(3).
						DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
						Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("invalid delayedTopologyRequest",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
								Request(corev1.ResourceCPU, "1").
								Obj(),
						).
						Obj()
				},
				utiltestingapi.MakeAdmission("cluster-queue").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flv", "3").
						Count(3).
						DelayedTopologyRequest("invalid").
						Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
		)

		ginkgo.DescribeTable("Should have valid values when setting AdmissionCheckState", func(w func() *kueue.Workload, acs kueue.AdmissionCheckState) {
			wl := w()
			util.MustCreate(ctx, k8sClient, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&wl.Status.AdmissionChecks, acs, util.RealClock)
				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(utiltesting.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},
			ginkgo.Entry("mismatched names in podSetUpdates with names in podSets",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("first", 1).Obj(),
							*utiltestingapi.MakePodSet("second", 1).Obj(),
						).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: "first"}, {Name: "third"}}},
			),
			ginkgo.Entry("invalid label name of podSetUpdate",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, Labels: map[string]string{"@abc": "foo"}}}},
			),
			ginkgo.Entry("invalid node selector name of podSetUpdate",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, NodeSelector: map[string]string{"@abc": "foo"}}}},
			),
			ginkgo.Entry("invalid label value of podSetUpdate",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, Labels: map[string]string{"foo": "@abc"}}}},
			),
		)

		ginkgo.It("invalid reclaimablePods", func() {
			ginkgo.By("Creating a new Workload")
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			err := workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 4},
				{Name: "ps2", Count: 1},
			})
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})
	})

	ginkgo.Context("When updating a Workload", func() {
		var (
			updatedQueueWorkload  kueue.Workload
			finalQueueWorkload    kueue.Workload
			workloadPriorityClass *kueue.WorkloadPriorityClass
			priorityClass         *schedulingv1.PriorityClass
		)
		ginkgo.BeforeEach(func() {
			workloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			priorityClass = utiltesting.MakePriorityClass("priority-class").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
			util.MustCreate(ctx, k8sClient, priorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate Workload on update",
			func(w func() *kueue.Workload, setQuotaReservation bool, updateWl func(newWL *kueue.Workload), matcher gomegatypes.GomegaMatcher) {
				ginkgo.By("Creating a new Workload")
				workload := w()
				util.MustCreate(ctx, k8sClient, workload)
				if setQuotaReservation {
					util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(workload), utiltestingapi.MakeAdmission("cq").Obj())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, workload)
				}
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
					updateWl(&newWL)
					g.Expect(k8sClient.Update(ctx, &newWL)).Should(matcher)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			},
			ginkgo.Entry("podSets should not be updated when has quota reservation: count",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets = []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Obj()}
				},
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("podSets should not be updated: podSpec",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets = []kueue.PodSet{{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "c-after",
										Resources: corev1.ResourceRequirements{
											Requests: make(corev1.ResourceList),
										},
									},
								},
							},
						},
					}}
				},
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("queueName can be updated when not admitted",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Queue("q1").Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.QueueName = "q2"
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("queueName can be updated when admitting",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.QueueName = "q"
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("queueName should not be updated once admitted",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Queue("q1").Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.QueueName = "q2"
				},
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("queueName can be updated when admission is reset",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Queue("q1").
						ReserveQuota(utiltestingapi.MakeAdmission("cq").Obj()).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.QueueName = "q2"
					newWL.Status = kueue.WorkloadStatus{}
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("admission can be set",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Status = kueue.WorkloadStatus{
						Admission: utiltestingapi.MakeAdmission("cluster-queue").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment("on-demand", "5", "1").Obj()).Obj(),
						Conditions: []metav1.Condition{{
							Type:               kueue.WorkloadQuotaReserved,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(time.Now()),
							Reason:             "AdmittedByTest",
							Message:            "Admitted by ClusterQueue cluster-queue",
						}},
					}
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("admission can be unset",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).ReserveQuota(
						utiltestingapi.MakeAdmission("cluster-queue").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment("on-demand", "5", "1").Obj()).Obj(),
					).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Status = kueue.WorkloadStatus{}
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("should change other fields of admissionchecks when podSetUpdates is immutable",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("first", 1).Obj(),
							*utiltestingapi.MakePodSet("second", 1).Obj(),
						).AdmissionChecks(
						kueue.AdmissionCheckState{
							Name:          "ac1",
							Message:       "old",
							PodSetUpdates: []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
							State:         kueue.CheckStateReady,
						}).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Status.AdmissionChecks = []kueue.AdmissionCheckState{
						{
							Name:               "ac1",
							Message:            "new",
							LastTransitionTime: metav1.NewTime(time.Now()),
							PodSetUpdates:      []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
							State:              kueue.CheckStateReady,
						},
					}
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("updating podSets before setting reserve quota for workload",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets = []kueue.PodSet{
						{
							Name:  kueue.DefaultPodSetName,
							Count: 1,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name: "c-after",
											Resources: corev1.ResourceRequirements{
												Requests: make(corev1.ResourceList),
											},
										},
									},
								},
							},
						},
					}
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("Should forbid the change of spec.podSet",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets[0].Count = 10
				},
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("reclaimable pod count can go to 0 if the job is suspended",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*utiltestingapi.MakePodSet("ps1", 3).Obj(),
							*utiltestingapi.MakePodSet("ps2", 3).Obj(),
						).
						ReserveQuota(
							utiltestingapi.MakeAdmission("cluster-queue").
								PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
								Obj(),
						).
						ReclaimablePods(
							kueue.ReclaimablePod{Name: "ps1", Count: 2},
							kueue.ReclaimablePod{Name: "ps2", Count: 1},
						).
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Status.AdmissionChecks = []kueue.AdmissionCheckState{
						{
							PodSetUpdates: []kueue.PodSetUpdate{{Name: "ps1"}, {Name: "ps2"}},
							State:         kueue.CheckStateReady,
						},
					}
					newWL.Status.ReclaimablePods = []kueue.ReclaimablePod{
						{Name: "ps1", Count: 0},
						{Name: "ps2", Count: 1},
					}
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can add maximum execution time when not admitted",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.MaximumExecutionTimeSeconds = ptr.To[int32](1)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can update maximum execution time when not admitted",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						MaximumExecutionTimeSeconds(1).
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					*newWL.Spec.MaximumExecutionTimeSeconds = 2
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("cannot add maximum execution time when admitted",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.MaximumExecutionTimeSeconds = ptr.To[int32](1)
				},
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("cannot update maximum execution time when admitted",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						MaximumExecutionTimeSeconds(1).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					*newWL.Spec.MaximumExecutionTimeSeconds = 2
				},
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("Should allow to change priority when QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.Priority = ptr.To[int32](10)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("Should allow to change priority when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.Priority = ptr.To[int32](10)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("Should allow to change priority with priorityClassRef when QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Priority(0).
						PodPriorityClassRef("low").
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.Priority = ptr.To[int32](10)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("Should allow to change priority with priorityClassRef when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						Priority(0).
						PodPriorityClassRef("low").
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.Priority = ptr.To[int32](10)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can set workload priorityClassRef when QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = kueue.NewWorkloadPriorityClassRef("low")
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can't set workload priority class when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = kueue.NewWorkloadPriorityClassRef("low")
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("can update workload priority class when QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						WorkloadPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef.Name = "low"
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can update workload priority class when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						WorkloadPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef.Name = "low"
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can delete workload priority class when QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						WorkloadPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = nil
					newWL.Spec.Priority = nil
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can't delete workload priority class when quota reserved when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						WorkloadPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = nil
					newWL.Spec.Priority = nil
				},
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("can set pod priority class QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = kueue.NewPodPriorityClassRef("low")
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can't set pod priority class when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = kueue.NewPodPriorityClassRef("low")
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("can update pod priority class when QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef.Name = "low"
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can't update pod priority class when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef.Name = "low"
					newWL.Spec.Priority = ptr.To[int32](100)
				},
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("can delete pod priority class when QuotaReserved=false",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = nil
					newWL.Spec.Priority = nil
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can't delete pod priority class when QuotaReserved=true",
				func() *kueue.Workload {
					return utiltestingapi.MakeWorkload(workloadName, ns.Name).
						PodPriorityClassRef("high").
						Priority(1000).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassRef = nil
					newWL.Spec.Priority = nil
				},
				utiltesting.BeInvalidError(),
			),
		)

		ginkgo.It("Should forbid the change of spec.queueName of an admitted workload", func() {
			ginkgo.By("Creating and admitting a new Workload")
			workload := utiltestingapi.MakeWorkload(workloadName, ns.Name).Queue("queue1").Obj()
			util.MustCreate(ctx, k8sClient, workload)
			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(workload), utiltestingapi.MakeAdmission("cq").Obj())

			ginkgo.By("Updating queueName")
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.QueueName = "queue2"
				g.Expect(k8sClient.Update(ctx, &newWL)).Should(utiltesting.BeInvalidError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should forbid the change of status.admission", func() {
			ginkgo.By("Creating a new Workload")
			workload := utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, workload)

			ginkgo.By("Admitting the Workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(workload), utiltestingapi.MakeAdmission("cluster-queue").Obj())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, workload)
			})

			ginkgo.By("Updating queueName")
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				g.Expect(newWL.Status.Admission).NotTo(gomega.BeNil())
				newWL.Status.Admission.ClusterQueue = "foo-cluster-queue"
				g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(utiltesting.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should have priority once priorityClassName is set", func() {
			ginkgo.By("Creating a new Workload")
			workload := utiltestingapi.MakeWorkload(workloadName, ns.Name).PodPriorityClassRef("priority").Obj()
			err := k8sClient.Create(ctx, workload)
			gomega.Expect(err).Should(utiltesting.BeInvalidError())
		})

		ginkgo.It("workload's priority should be mutable when referencing WorkloadPriorityClass", func() {
			ginkgo.By("creating workload")
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				WorkloadPriorityClassRef("workload-priority-class").
				Priority(200).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				g.Expect(updatedQueueWorkload.Status.Conditions).ShouldNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			initialPriority := int32(200)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("Updating workload's priority")
			updatedPriority := int32(150)
			updatedQueueWorkload.Spec.Priority = &updatedPriority
			gomega.Expect(k8sClient.Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
				g.Expect(finalQueueWorkload.Spec.Priority).Should(gomega.Equal(&updatedPriority))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("workload's priority should be mutable when referencing PriorityClass", func() {
			ginkgo.By("creating workload")
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PodPriorityClassRef("priority-class").
				Priority(100).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				g.Expect(updatedQueueWorkload.Status.Conditions).ShouldNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			initialPriority := int32(100)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("Updating workload's priority")
			updatedPriority := int32(50)
			updatedQueueWorkload.Spec.Priority = &updatedPriority
			gomega.Expect(k8sClient.Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
				g.Expect(finalQueueWorkload.Spec.Priority).Should(gomega.Equal(&updatedPriority))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("admission should not be updated once set", func() {
			ginkgo.By("Creating a new Workload")
			workload := utiltestingapi.MakeWorkload(workloadName, ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, workload)
			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(workload), utiltestingapi.MakeAdmission("cluster-queue").Obj())

			ginkgo.By("Updating the workload setting admission")
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission = utiltestingapi.MakeAdmission("cluster-queue").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment("on-demand", "5", "1").Obj()).Obj()
				g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(utiltesting.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("reclaimable pod count can change up", func() {
			ginkgo.By("Creating a new Workload")
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 1},
			})).Should(gomega.Succeed())

			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl),
				utiltestingapi.MakeAdmission("cluster-queue").
					PodSets(
						kueue.PodSetAssignment{Name: "ps1"},
						kueue.PodSetAssignment{Name: "ps2"}).
					Obj())

			ginkgo.By("Updating reclaimable pods")
			err := workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 2},
				{Name: "ps2", Count: 1},
			})
			gomega.Expect(err).Should(gomega.Succeed())
		})

		ginkgo.It("reclaimable pod count cannot change down", func() {
			ginkgo.By("Creating a new Workload")
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 2},
				{Name: "ps2", Count: 1},
			})).Should(gomega.Succeed())

			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl),
				utiltestingapi.MakeAdmission("cluster-queue").
					PodSets(
						kueue.PodSetAssignment{Name: "ps1"},
						kueue.PodSetAssignment{Name: "ps2"}).
					Obj())

			ginkgo.By("Updating reclaimable pods")
			err := workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 1},
			})
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})

		ginkgo.It("podSetUpdates should be immutable when state is ready", func() {
			ginkgo.By("Creating a new Workload")
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*utiltestingapi.MakePodSet("first", 1).Obj(),
					*utiltestingapi.MakePodSet("second", 1).Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Setting admission check state")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:               "ac1",
					Message:            "old",
					LastTransitionTime: metav1.NewTime(time.Now()),
					PodSetUpdates:      []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
					State:              kueue.CheckStateReady,
				}, util.RealClock)
				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating admission check state")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:               "ac1",
					Message:            "new",
					LastTransitionTime: metav1.NewTime(time.Now()),
					PodSetUpdates:      []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "baz"}}, {Name: "second"}},
					State:              kueue.CheckStateReady,
				}, util.RealClock)
				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(utiltesting.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should not allow workload podSets count increase even when ElasticJobsViaWorkloadSlices feature gate is enabled", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

			ginkgo.By("Creating a new Workload")
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Admitting the Workload")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				admission := utiltestingapi.MakeAdmission("default").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj()
				workload.SetQuotaReservation(wl, admission, util.RealClock)
				wl.Status.Admission = admission

				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating PodSets count")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				wl.Spec.PodSets[0].Count++ // Increase from 1 -> 2.
				g.Expect(k8sClient.Update(ctx, wl)).Should(utiltesting.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.It("Should allow workload podSets count decrease when ElasticJobsViaWorkloadSlices feature gate is enabled", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

			ginkgo.By("Creating a new Workload")
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Admitting the Workload")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				admission := utiltestingapi.MakeAdmission("default").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Count(10).
						Obj()).
					Obj()
				workload.SetQuotaReservation(wl, admission, util.RealClock)
				wl.Status.Admission = admission

				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating PodSets count")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				wl.Spec.PodSets[0].Count = 5 // Decrease from 10 -> 5.
				g.Expect(k8sClient.Update(ctx, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("Workload validating webhook ClusterName - Dispatcher AllAtOnce", ginkgo.Ordered, func() {
	var _ = ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	var _ = ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.Context("When updating a Workload", func() {
		var (
			workloadPriorityClass *kueue.WorkloadPriorityClass
			priorityClass         *schedulingv1.PriorityClass
		)
		ginkgo.BeforeEach(func() {
			workloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			priorityClass = utiltesting.MakePriorityClass("priority-class").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
			util.MustCreate(ctx, k8sClient, priorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate Workload status on update",
			func(setupWlStatus func(w *kueue.Workload), updateWlStatus func(w *kueue.Workload), setupMatcher, updateMatcher gomegatypes.GomegaMatcher) {
				ginkgo.By("Creating a new Workload")
				workload := utiltestingapi.MakeWorkloadWithGeneratedName(workloadName, ns.Name).Obj()
				util.MustCreate(ctx, k8sClient, workload)

				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &wl)).To(gomega.Succeed())
					setupWlStatus(&wl)
					g.Expect(k8sClient.Status().Update(ctx, &wl)).Should(setupMatcher)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &wl)).To(gomega.Succeed())
					updateWlStatus(&wl)
					g.Expect(k8sClient.Status().Update(ctx, &wl)).Should(updateMatcher)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			},
			ginkgo.Entry("Valid: ClusterName is in NominatedClusters",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = ptr.To("worker2")
				},
				gomega.Succeed(),
				gomega.Succeed(),
			),
			ginkgo.Entry("Invalid: ClusterName is not in NominatedClusters",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = ptr.To("worker3")
				},
				gomega.Succeed(),
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("Invalid: ClusterName is set when NominatedClusters is empty",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = ptr.To("worker1")
					wl.Status.NominatedClusterNames = nil
				},
				gomega.Succeed(),
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("Invalid: ClusterName and NominatedClusters are mutually exclusive",
				func(wl *kueue.Workload) {},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = ptr.To("worker1")
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
				},
				gomega.Succeed(),
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("Valid: neither ClusterName nor NominatedClusters is set",
				func(wl *kueue.Workload) {},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = nil
					wl.Status.NominatedClusterNames = nil
				},
				gomega.Succeed(),
				gomega.Succeed(),
			),
		)
	})
})

var _ = ginkgo.Describe("Workload validating webhook ClusterName - Dispatcher Incremental", ginkgo.Ordered, func() {
	var _ = ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	var _ = ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.Context("When updating a Workload", func() {
		var (
			workloadPriorityClass *kueue.WorkloadPriorityClass
			priorityClass         *schedulingv1.PriorityClass
		)
		ginkgo.BeforeEach(func() {
			workloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			priorityClass = utiltesting.MakePriorityClass("priority-class").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
			util.MustCreate(ctx, k8sClient, priorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate Workload status on update",
			func(setupWlStatus func(w *kueue.Workload), updateWlStatus func(w *kueue.Workload), setupMatcher, updateMatcher gomegatypes.GomegaMatcher) {
				ginkgo.By("Creating a new Workload")
				workload := utiltestingapi.MakeWorkloadWithGeneratedName(workloadName, ns.Name).Obj()
				util.MustCreate(ctx, k8sClient, workload)

				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &wl)).To(gomega.Succeed())
					setupWlStatus(&wl)
					g.Expect(k8sClient.Status().Update(ctx, &wl)).Should(setupMatcher)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &wl)).To(gomega.Succeed())
					updateWlStatus(&wl)
					g.Expect(k8sClient.Status().Update(ctx, &wl)).Should(updateMatcher)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			},
			ginkgo.Entry("Valid: ClusterName is in NominatedClusters",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = ptr.To("worker2")
				},
				gomega.Succeed(),
				gomega.Succeed(),
			),
			ginkgo.Entry("Invalid: ClusterName is not in NominatedClusters",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = ptr.To("worker3")
				},
				gomega.Succeed(),
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("Invalid: ClusterName is changed",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = ptr.To("worker1")
				},
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = ptr.To("worker2")
				},
				utiltesting.BeForbiddenError(),
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("Invalid: ClusterName is set when NominatedClusters is empty",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = ptr.To("worker1")
					wl.Status.NominatedClusterNames = nil
				},
				gomega.Succeed(),
				utiltesting.BeForbiddenError(),
			),
			ginkgo.Entry("Invalid: ClusterName and NominatedClusters are mutually exclusive",
				func(wl *kueue.Workload) {},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = ptr.To("worker1")
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
				},
				gomega.Succeed(),
				utiltesting.BeInvalidError(),
			),
			ginkgo.Entry("Valid: neither ClusterName nor NominatedClusters is set",
				func(wl *kueue.Workload) {},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = nil
					wl.Status.NominatedClusterNames = nil
				},
				gomega.Succeed(),
				gomega.Succeed(),
			),
		)

		ginkgo.It("Should allow to set ClusterName without previous nominatedClusterNames when ElasticJobsViaWorkloadSlices feature gate is enabled", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

			ginkgo.By("Creating a new Workload")
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.NewReference(ns.Name, workloadName))).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("mimic scheduler by setting the status.clusterName during quota reservation")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				wl.Status.Admission = utiltestingapi.MakeAdmission("default").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj()
				wl.Status.ClusterName = ptr.To("worker1")
				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
