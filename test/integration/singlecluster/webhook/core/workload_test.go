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
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var ns *corev1.Namespace

const (
	workloadName    = "workload-test"
	podSetsMaxItems = 8
)

var _ = ginkgo.BeforeEach(func() {
	ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
})

var _ = ginkgo.AfterEach(func() {
	gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
})

var _ = ginkgo.Describe("Workload defaulting webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr, config.MultiKueueDispatcherModeAllAtOnce)
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
						*testing.MakePodSet("", 1).
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
						*testing.MakePodSet("", 1).
							Containers(corev1.Container{}).
							Obj(),
						*testing.MakePodSet("", 1).
							Containers(corev1.Container{}).
							Obj(),
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, &workload)).Should(testing.BeInvalidError())
		})
	})
})

var _ = ginkgo.Describe("Workload validating webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var realClock = clock.RealClock{}

	ginkgo.Context("When creating a Workload", func() {
		ginkgo.DescribeTable("Should have valid PodSet when creating", func(podSetsCapacity int, podSetCount int, isInvalid bool) {
			podSets := make([]kueue.PodSet, podSetsCapacity)
			for i := range podSets {
				podSets[i] = *testing.MakePodSet(kueue.NewPodSetReference(fmt.Sprintf("ps%d", i)), podSetCount).Obj()
			}
			workload := testing.MakeWorkload(workloadName, ns.Name).PodSets(podSets...).Obj()
			err := k8sClient.Create(ctx, workload)
			if isInvalid {
				gomega.Expect(err).Should(testing.BeInvalidError())
			} else {
				gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			}
		},
			ginkgo.Entry("podSets count less than 1", 0, 1, true),
			ginkgo.Entry("podSets count more than 8", podSetsMaxItems+1, 1, true),
			ginkgo.Entry("valid podSet, count can be 0", 3, 0, false),
			ginkgo.Entry("valid podSet", 3, 3, false),
		)

		ginkgo.DescribeTable("Should have valid values when creating", func(w func() *kueue.Workload, errorType gomega.OmegaMatcher) {
			err := k8sClient.Create(ctx, w())
			if errorType != nil {
				gomega.Expect(err).Should(errorType)
			} else {
				gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			}
		},
			ginkgo.Entry("valid workload",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).PodSets(
						*testing.MakePodSet("driver", 1).Obj(),
						*testing.MakePodSet("workers", 100).Obj(),
					).Obj()
				},
				nil),
			ginkgo.Entry("invalid podSet name",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).PodSets(
						*testing.MakePodSet("@driver", 1).Obj(),
					).Obj()
				},
				testing.BeInvalidError()),
			ginkgo.Entry("invalid priorityClassName",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PriorityClass("invalid_class").
						Priority(0).
						Obj()
				},
				testing.BeInvalidError()),
			ginkgo.Entry("empty priorityClassName is valid",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				nil),
			ginkgo.Entry("priority should not be nil when priorityClassName is set",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PriorityClass("priority").
						Obj()
				},
				testing.BeInvalidError()),
			ginkgo.Entry("invalid queueName",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Queue("@invalid").
						Obj()
				},
				testing.BeInvalidError()),
			ginkgo.Entry("should not request num-pods resource",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("bad", 1).
								InitContainers(
									testing.SingleContainerForRequest(map[corev1.ResourceName]string{
										corev1.ResourcePods: "1",
									})...,
								).
								Containers(
									testing.SingleContainerForRequest(map[corev1.ResourceName]string{
										corev1.ResourcePods: "1",
									})...,
								).
								Obj(),
						).
						Obj()
				},
				testing.BeForbiddenError()),
			ginkgo.Entry("empty podSetUpdates should be valid since it is optional",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						AdmissionChecks(kueue.AdmissionCheckState{}).
						Obj()
				},
				nil),
			ginkgo.Entry("matched names in podSetUpdates with names in podSets",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("first", 1).Obj(),
							*testing.MakePodSet("second", 1).Obj(),
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
				nil),
			ginkgo.Entry("invalid podSet minCount (negative)",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("ps1", 3).SetMinimumCount(-1).Obj(),
						).
						Obj()
				},
				testing.BeInvalidError()),
			ginkgo.Entry("invalid podSet minCount (too big)",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("ps1", 3).SetMinimumCount(4).Obj(),
						).
						Obj()
				},
				testing.BeInvalidError()),
			ginkgo.Entry("too many variable count podSets",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("ps1", 3).SetMinimumCount(2).Obj(),
							*testing.MakePodSet("ps2", 3).SetMinimumCount(1).Obj(),
						).
						Obj()
				},
				testing.BeForbiddenError()),
			ginkgo.Entry("invalid maximumExexcutionTimeSeconds",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						MaximumExecutionTimeSeconds(0).
						Obj()
				},
				testing.BeInvalidError()),
			ginkgo.Entry("valid maximumExexcutionTimeSeconds",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						MaximumExecutionTimeSeconds(1).
						Obj()
				},
				nil),
		)

		ginkgo.DescribeTable("Should have valid values when setting Admission", func(w func() *kueue.Workload, a *kueue.Admission, errorType gomega.OmegaMatcher) {
			workload := w()
			util.MustCreate(ctx, k8sClient, workload)

			err := util.SetQuotaReservation(ctx, k8sClient, workload, a)
			if errorType != nil {
				gomega.Expect(err).Should(errorType)
			} else {
				gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			}
		},
			ginkgo.Entry("invalid clusterQueue name",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				testing.MakeAdmission("@invalid").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("invalid podSet name in status assignment",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				testing.MakeAdmission("cluster-queue", "@invalid").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("mismatched names in admission with names in podSets",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("main2", 1).Obj(),
							*testing.MakePodSet("main1", 1).Obj(),
						).
						Obj()
				},
				testing.MakeAdmission("cluster-queue", "main1", "main2", "main3").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("assignment usage should be divisible by count",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet(kueue.DefaultPodSetName, 3).
								Request(corev1.ResourceCPU, "1").
								Obj(),
						).
						Obj()
				},
				testing.MakeAdmission("cluster-queue").
					Assignment(corev1.ResourceCPU, "flv", "1").
					AssignmentPodCount(3).
					Obj(),
				testing.BeForbiddenError()),
		)

		ginkgo.DescribeTable("Should have valid values when setting AdmissionCheckState", func(w func() *kueue.Workload, acs kueue.AdmissionCheckState) {
			wl := w()
			util.MustCreate(ctx, k8sClient, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&wl.Status.AdmissionChecks, acs, realClock)
				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(testing.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},
			ginkgo.Entry("mismatched names in podSetUpdates with names in podSets",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("first", 1).Obj(),
							*testing.MakePodSet("second", 1).Obj(),
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
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, Labels: map[string]string{"@abc": "foo"}}}},
			),
			ginkgo.Entry("invalid node selector name of podSetUpdate",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, NodeSelector: map[string]string{"@abc": "foo"}}}},
			),
			ginkgo.Entry("invalid label value of podSetUpdate",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
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
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*testing.MakePodSet("ps1", 3).Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			err := workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 4},
				{Name: "ps2", Count: 1},
			})
			gomega.Expect(err).Should(testing.BeForbiddenError())
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
			workloadPriorityClass = testing.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			priorityClass = testing.MakePriorityClass("priority-class").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
			util.MustCreate(ctx, k8sClient, priorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate Workload on update",
			func(w func() *kueue.Workload, setQuotaReservation bool, updateWl func(newWL *kueue.Workload), matcher gomega.OmegaMatcher) {
				ginkgo.By("Creating a new Workload")
				workload := w()
				util.MustCreate(ctx, k8sClient, workload)
				if setQuotaReservation {
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, workload, testing.MakeAdmission("cq").Obj())).Should(gomega.Succeed())
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
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets = []kueue.PodSet{*testing.MakePodSet(kueue.DefaultPodSetName, 2).Obj()}
				},
				testing.BeForbiddenError(),
			),
			ginkgo.Entry("podSets should not be updated: podSpec",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
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
				testing.BeForbiddenError(),
			),
			ginkgo.Entry("queueName can be updated when not admitted",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Queue("q1").Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.QueueName = "q2"
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("queueName can be updated when admitting",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.QueueName = "q"
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("queueName should not be updated once admitted",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Queue("q1").Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.QueueName = "q2"
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("queueName can be updated when admission is reset",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Queue("q1").
						ReserveQuota(testing.MakeAdmission("cq").Obj()).Obj()
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
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Status = kueue.WorkloadStatus{
						Admission: testing.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
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
					return testing.MakeWorkload(workloadName, ns.Name).ReserveQuota(
						testing.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
					).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Status = kueue.WorkloadStatus{}
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("priorityClassSource should not be updated",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Queue("q").
						PriorityClass("test-class").PriorityClassSource(constants.PodPriorityClassSource).
						Priority(10).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassSource = constants.WorkloadPriorityClassSource
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("priorityClassName should not be updated",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Queue("q").
						PriorityClass("test-class-1").PriorityClassSource(constants.PodPriorityClassSource).
						Priority(10).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassName = "test-class-2"
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("should change other fields of admissionchecks when podSetUpdates is immutable",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("first", 1).Obj(),
							*testing.MakePodSet("second", 1).Obj(),
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
			ginkgo.Entry("updating priorityClassName before setting reserve quota for workload",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Queue("q").
						PriorityClass("test-class-1").PriorityClassSource(constants.PodPriorityClassSource).
						Priority(10).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassName = "test-class-2"
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("updating priorityClassSource before setting reserve quota for workload",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Queue("q").
						PriorityClass("test-class").PriorityClassSource(constants.PodPriorityClassSource).
						Priority(10).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.PriorityClassSource = constants.WorkloadPriorityClassSource
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("updating podSets before setting reserve quota for workload",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
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
			ginkgo.Entry("Should allow the change of priority",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.Priority = ptr.To[int32](10)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("Should forbid the change of spec.podSet",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets[0].Count = 10
				},
				testing.BeForbiddenError(),
			),
			ginkgo.Entry("reclaimable pod count can go to 0 if the job is suspended",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("ps1", 3).Obj(),
							*testing.MakePodSet("ps2", 3).Obj(),
						).
						ReserveQuota(
							testing.MakeAdmission("cluster-queue").
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
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				false,
				func(newWL *kueue.Workload) {
					newWL.Spec.MaximumExecutionTimeSeconds = ptr.To[int32](1)
				},
				gomega.Succeed(),
			),
			ginkgo.Entry("can update maximum execution time when not admitted",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
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
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.MaximumExecutionTimeSeconds = ptr.To[int32](1)
				},
				testing.BeInvalidError(),
			),
			ginkgo.Entry("cannot update maximum execution time when admitted",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						MaximumExecutionTimeSeconds(1).
						Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					*newWL.Spec.MaximumExecutionTimeSeconds = 2
				},
				testing.BeInvalidError(),
			),
		)

		ginkgo.It("Should forbid the change of spec.queueName of an admitted workload", func() {
			ginkgo.By("Creating and admitting a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Queue("queue1").Obj()
			util.MustCreate(ctx, k8sClient, workload)
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(ctx, k8sClient, &newWL, testing.MakeAdmission("cq").Obj())).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			ginkgo.By("Updating queueName")
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.QueueName = "queue2"
				g.Expect(k8sClient.Update(ctx, &newWL)).Should(testing.BeInvalidError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should forbid the change of spec.admission", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, workload)

			ginkgo.By("Admitting the Workload")
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission = testing.MakeAdmission("cluster-queue").Obj()
				g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating queueName")
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission.ClusterQueue = "foo-cluster-queue"
				g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(testing.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should have priority once priorityClassName is set", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).PriorityClass("priority").Obj()
			err := k8sClient.Create(ctx, workload)
			gomega.Expect(err).Should(testing.BeInvalidError())
		})

		ginkgo.It("workload's priority should be mutable when referencing WorkloadPriorityClass", func() {
			ginkgo.By("creating workload")
			wl := testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("workload-priority-class").PriorityClassSource(constants.WorkloadPriorityClassSource).Priority(200).Obj()
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
			wl := testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("priority-class").PriorityClassSource(constants.PodPriorityClassSource).Priority(100).Obj()
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
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, workload)
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, workload, testing.MakeAdmission("cluster-queue").Obj())).Should(gomega.Succeed())

			ginkgo.By("Updating the workload setting admission")
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission = testing.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj()
				g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(testing.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("reclaimable pod count can change up", func() {
			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*testing.MakePodSet("ps1", 3).Obj(),
					*testing.MakePodSet("ps2", 3).Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 1},
			})).Should(gomega.Succeed())

			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl,
				testing.MakeAdmission("cluster-queue").
					PodSets(
						kueue.PodSetAssignment{Name: "ps1"},
						kueue.PodSetAssignment{Name: "ps2"}).
					Obj())).Should(gomega.Succeed())

			ginkgo.By("Updating reclaimable pods")
			err := workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 2},
				{Name: "ps2", Count: 1},
			})
			gomega.Expect(err).Should(gomega.Succeed())
		})

		ginkgo.It("reclaimable pod count cannot change down", func() {
			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*testing.MakePodSet("ps1", 3).Obj(),
					*testing.MakePodSet("ps2", 3).Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 2},
				{Name: "ps2", Count: 1},
			})).Should(gomega.Succeed())

			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl,
				testing.MakeAdmission("cluster-queue").
					PodSets(
						kueue.PodSetAssignment{Name: "ps1"},
						kueue.PodSetAssignment{Name: "ps2"}).
					Obj())).Should(gomega.Succeed())

			ginkgo.By("Updating reclaimable pods")
			err := workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 1},
			})
			gomega.Expect(err).Should(testing.BeForbiddenError())
		})

		ginkgo.It("podSetUpdates should be immutable when state is ready", func() {
			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*testing.MakePodSet("first", 1).Obj(),
					*testing.MakePodSet("second", 1).Obj(),
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
				}, realClock)
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
				}, realClock)
				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(testing.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should not allow workload podSets count increase even when ElasticJobsViaWorkloadSlices feature gate is enabled", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(*testing.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Admitting the Workload")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				admission := testing.MakeAdmission("default").
					Assignment(corev1.ResourceCPU, "default", "1").
					AssignmentPodCount(1).
					Obj()
				workload.SetQuotaReservation(wl, admission, realClock)
				wl.Status.Admission = admission

				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating PodSets count")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				wl.Spec.PodSets[0].Count++ // Increase from 1 -> 2.
				g.Expect(k8sClient.Update(ctx, wl)).Should(testing.BeForbiddenError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.It("Should allow workload podSets count decrease when ElasticJobsViaWorkloadSlices feature gate is enabled", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(*testing.MakePodSet(kueue.DefaultPodSetName, 10).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Admitting the Workload")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				admission := testing.MakeAdmission("default").
					Assignment(corev1.ResourceCPU, "default", "1").
					AssignmentPodCount(10).
					Obj()
				workload.SetQuotaReservation(wl, admission, realClock)
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
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr, config.MultiKueueDispatcherModeAllAtOnce)
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
			workloadPriorityClass = testing.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			priorityClass = testing.MakePriorityClass("priority-class").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
			util.MustCreate(ctx, k8sClient, priorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate Workload status on update",
			func(setupWlStatus func(w *kueue.Workload), updateWlStatus func(w *kueue.Workload), setupMatcher, updateMatcher gomega.OmegaMatcher) {
				ginkgo.By("Creating a new Workload")
				workload := testing.MakeWorkloadWithGeneratedName(workloadName, ns.Name).Obj()
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
			ginkgo.Entry("Valid: ClusterName is not in NominatedClusters",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = ptr.To("worker3")
				},
				gomega.Succeed(),
				gomega.Succeed(),
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
				gomega.Succeed(),
				testing.BeInvalidError(),
			),
			ginkgo.Entry("Valid: ClusterName is set when NominatedClusters is empty",
				func(wl *kueue.Workload) {
					wl.Status.NominatedClusterNames = nil
					wl.Status.ClusterName = nil
				},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = ptr.To("worker1")
					wl.Status.NominatedClusterNames = nil
				},
				gomega.Succeed(),
				gomega.Succeed(),
			),
			ginkgo.Entry("Invalid: ClusterName and NominatedClusters are mutually exclusive",
				func(wl *kueue.Workload) {},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = ptr.To("worker1")
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
				},
				gomega.Succeed(),
				testing.BeInvalidError(),
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
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr, config.MultiKueueDispatcherModeIncremental)
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
			workloadPriorityClass = testing.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			priorityClass = testing.MakePriorityClass("priority-class").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
			util.MustCreate(ctx, k8sClient, priorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate Workload status on update",
			func(setupWlStatus func(w *kueue.Workload), updateWlStatus func(w *kueue.Workload), setupMatcher, updateMatcher gomega.OmegaMatcher) {
				ginkgo.By("Creating a new Workload")
				workload := testing.MakeWorkloadWithGeneratedName(workloadName, ns.Name).Obj()
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
				testing.BeForbiddenError(),
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
				testing.BeForbiddenError(),
				testing.BeForbiddenError(),
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
				testing.BeForbiddenError(),
			),
			ginkgo.Entry("Invalid: ClusterName and NominatedClusters are mutually exclusive",
				func(wl *kueue.Workload) {},
				func(wl *kueue.Workload) {
					wl.Status.ClusterName = ptr.To("worker1")
					wl.Status.NominatedClusterNames = []string{"worker1", "worker2"}
				},
				gomega.Succeed(),
				testing.BeInvalidError(),
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
