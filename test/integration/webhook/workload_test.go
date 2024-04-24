/*
Copyright 2022 The Kubernetes Authors.
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

package webhook

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
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
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "core-",
		},
	}
	gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
})

var _ = ginkgo.AfterEach(func() {
	gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
})

var _ = ginkgo.Describe("Workload defaulting webhook", func() {
	ginkgo.Context("When creating a Workload", func() {
		ginkgo.It("Should set default podSet name", func() {
			ginkgo.By("Creating a new Workload")
			// Not using the wrappers to avoid hiding any defaulting.
			workload := kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: ns.Name},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{
							Count: 1,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{},
								},
							},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, &workload)).Should(gomega.Succeed())

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
						{
							Count: 1,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{},
								},
							},
						},
						{
							Count: 1,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{},
								},
							},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, &workload)).Should(testing.BeAPIError(testing.InvalidError))
		})

	})
})

var _ = ginkgo.Describe("Workload validating webhook", func() {
	ginkgo.Context("When creating a Workload", func() {

		ginkgo.DescribeTable("Should have valid PodSet when creating", func(podSetsCapacity int, podSetCount int, isInvalid bool) {
			podSets := make([]kueue.PodSet, podSetsCapacity)
			for i := range podSets {
				podSets[i] = *testing.MakePodSet(fmt.Sprintf("ps%d", i), podSetCount).Obj()
			}
			workload := testing.MakeWorkload(workloadName, ns.Name).PodSets(podSets...).Obj()
			err := k8sClient.Create(ctx, workload)
			if isInvalid {
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(errors.IsInvalid(err)).Should(gomega.BeTrue(), "error: %v", err)
			} else {
				gomega.Expect(err).Should(gomega.Succeed())
			}
		},
			ginkgo.Entry("podSets count less than 1", 0, 1, true),
			ginkgo.Entry("podSets count more than 8", podSetsMaxItems+1, 1, true),
			ginkgo.Entry("invalid podSet.Count", 3, 0, true),
			ginkgo.Entry("valid podSet", 3, 3, false),
		)

		ginkgo.DescribeTable("Should have valid values when creating", func(w func() *kueue.Workload, errorType int) {
			err := k8sClient.Create(ctx, w())
			switch errorType {
			case isForbidden:
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(err).Should(testing.BeAPIError(testing.ForbiddenError), "error: %v", err)
			case isInvalid:
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(err).Should(testing.BeAPIError(testing.InvalidError), "error: %v", err)
			default:
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
				isValid),
			ginkgo.Entry("invalid podSet name",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).PodSets(
						*testing.MakePodSet("@driver", 1).Obj(),
					).Obj()
				},
				isInvalid),
			ginkgo.Entry("invalid priorityClassName",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PriorityClass("invalid_class").
						Priority(0).
						Obj()
				},
				isInvalid),
			ginkgo.Entry("empty priorityClassName is valid",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				isValid),
			ginkgo.Entry("priority should not be nil when priorityClassName is set",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PriorityClass("priority").
						Obj()
				},
				isInvalid),
			ginkgo.Entry("invalid queueName",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Queue("@invalid").
						Obj()
				},
				isInvalid),
			ginkgo.Entry("should not request num-pods resource",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(kueue.PodSet{
							Name:  "bad",
							Count: 1,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									InitContainers: []corev1.Container{
										{
											Resources: corev1.ResourceRequirements{
												Requests: corev1.ResourceList{
													corev1.ResourcePods: resource.MustParse("1"),
												},
											},
										},
									},
									Containers: []corev1.Container{
										{
											Resources: corev1.ResourceRequirements{
												Requests: corev1.ResourceList{
													corev1.ResourcePods: resource.MustParse("1"),
												},
											},
										},
									},
								},
							},
						}).
						Obj()
				},
				isForbidden),
			ginkgo.Entry("empty podSetUpdates should be valid since it is optional",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						AdmissionChecks(kueue.AdmissionCheckState{}).
						Obj()
				},
				isValid),
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
				isValid),
			ginkgo.Entry("invalid podSet minCount (negative)",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("ps1", 3).SetMinimumCount(-1).Obj(),
						).
						Obj()
				},
				isInvalid),
			ginkgo.Entry("invalid podSet minCount (too big)",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("ps1", 3).SetMinimumCount(4).Obj(),
						).
						Obj()
				},
				isInvalid),
			ginkgo.Entry("too many variable count podSets",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("ps1", 3).SetMinimumCount(2).Obj(),
							*testing.MakePodSet("ps2", 3).SetMinimumCount(1).Obj(),
						).
						Obj()
				},
				isForbidden),
		)

		ginkgo.DescribeTable("Should have valid values when setting Admission", func(w func() *kueue.Workload, a *kueue.Admission, errorType int) {
			workload := w()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			err := util.SetQuotaReservation(ctx, k8sClient, workload, a)
			switch errorType {
			case isForbidden:
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(err).Should(testing.BeAPIError(testing.ForbiddenError), "error: %v", err)
			case isInvalid:
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(err).Should(testing.BeAPIError(testing.InvalidError), "error: %v", err)
			default:
				gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			}
		},
			ginkgo.Entry("invalid clusterQueue name",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				testing.MakeAdmission("@invalid").Obj(),
				isInvalid),
			ginkgo.Entry("invalid podSet name in status assignment",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				testing.MakeAdmission("cluster-queue", "@invalid").Obj(),
				isInvalid),
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
				isInvalid),
			ginkgo.Entry("assignment usage should be divisible by count",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						PodSets(
							*testing.MakePodSet("main", 3).
								Request(corev1.ResourceCPU, "1").
								Obj(),
						).
						Obj()
				},
				testing.MakeAdmission("cluster-queue").
					Assignment(corev1.ResourceCPU, "flv", "1").
					AssignmentPodCount(3).
					Obj(),
				isForbidden),
		)

		ginkgo.DescribeTable("Should have valid values when setting AdmissionCheckState", func(w func() *kueue.Workload, acs kueue.AdmissionCheckState) {
			wl := w()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&wl.Status.AdmissionChecks, acs)
				return k8sClient.Status().Update(ctx, wl)
			}, util.Timeout, util.Interval).Should(testing.BeAPIError(testing.ForbiddenError))

		},
			ginkgo.Entry("podSetUpdates have the same number of podSets",
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
					PodSetUpdates: []kueue.PodSetUpdate{{Name: "first"}}},
			),
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
					PodSetUpdates: []kueue.PodSetUpdate{{Name: "main", Labels: map[string]string{"@abc": "foo"}}}},
			),
			ginkgo.Entry("invalid node selector name of podSetUpdate",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: "main", NodeSelector: map[string]string{"@abc": "foo"}}}},
			),
			ginkgo.Entry("invalid label value of podSetUpdate",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).
						Obj()
				},
				kueue.AdmissionCheckState{
					Name:          "check",
					State:         kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{{Name: "main", Labels: map[string]string{"foo": "@abc"}}}},
			),
		)

		ginkgo.It("invalid reclaimablePods", func() {
			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*testing.MakePodSet("ps1", 3).Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

			err := workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: "ps1", Count: 4},
				{Name: "ps2", Count: 1},
			})
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeAPIError(testing.ForbiddenError), "error: %v", err)
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
			gomega.Expect(k8sClient.Create(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, priorityClass)).To(gomega.Succeed())

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
				gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())
				if setQuotaReservation {
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, workload, testing.MakeAdmission("cq").Obj())).Should(gomega.Succeed())
				}

				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
					updateWl(&newWL)
					return k8sClient.Update(ctx, &newWL)
				}, util.Timeout, util.Interval).Should(matcher)
			},
			ginkgo.Entry("podSets should not be updated when has quota reservation: count",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets = []kueue.PodSet{*testing.MakePodSet("main", 2).Obj()}
				},
				testing.BeAPIError(testing.InvalidError),
			),
			ginkgo.Entry("podSets should not be updated: podSpec",
				func() *kueue.Workload {
					return testing.MakeWorkload(workloadName, ns.Name).Obj()
				},
				true,
				func(newWL *kueue.Workload) {
					newWL.Spec.PodSets = []kueue.PodSet{{
						Name:  "main",
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
				testing.BeAPIError(testing.InvalidError),
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
				testing.BeAPIError(testing.InvalidError),
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
				testing.BeAPIError(testing.InvalidError),
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
				testing.BeAPIError(testing.InvalidError),
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
							Name:  "main",
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
				testing.BeAPIError(testing.InvalidError),
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
		)

		ginkgo.It("Should forbid the change of spec.queueName of an admitted workload", func() {
			ginkgo.By("Creating and admitting a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Queue("queue1").Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())
			gomega.EventuallyWithOffset(1, func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				return util.SetQuotaReservation(ctx, k8sClient, &newWL, testing.MakeAdmission("cq").Obj())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			ginkgo.By("Updating queueName")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.QueueName = "queue2"
				return k8sClient.Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(testing.BeAPIError(testing.InvalidError))
		})

		ginkgo.It("Should forbid the change of spec.admission", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Admitting the Workload")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission = testing.MakeAdmission("cluster-queue").Obj()
				return k8sClient.Status().Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating queueName")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission.ClusterQueue = "foo-cluster-queue"
				return k8sClient.Status().Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(testing.BeAPIError(testing.ForbiddenError))

		})

		ginkgo.It("Should have priority once priorityClassName is set", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).PriorityClass("priority").Obj()
			err := k8sClient.Create(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeAPIError(testing.InvalidError), "error: %v", err)
		})

		ginkgo.It("workload's priority should be mutable when referencing WorkloadPriorityClass", func() {
			ginkgo.By("creating workload")
			wl := testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("workload-priority-class").PriorityClassSource(constants.WorkloadPriorityClassSource).Priority(200).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, util.Timeout, util.Interval).ShouldNot(gomega.BeNil())
			initialPriority := int32(200)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("Updating workload's priority")
			updatedPriority := int32(150)
			updatedQueueWorkload.Spec.Priority = &updatedPriority
			gomega.Expect(k8sClient.Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
			gomega.Eventually(func() *int32 {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
				return finalQueueWorkload.Spec.Priority
			}, util.Timeout, util.Interval).Should(gomega.Equal(&updatedPriority))
		})

		ginkgo.It("workload's priority should be mutable when referencing PriorityClass", func() {
			ginkgo.By("creating workload")
			wl := testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("priority-class").PriorityClassSource(constants.PodPriorityClassSource).Priority(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, util.Timeout, util.Interval).ShouldNot(gomega.BeNil())
			initialPriority := int32(100)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("Updating workload's priority")
			updatedPriority := int32(50)
			updatedQueueWorkload.Spec.Priority = &updatedPriority
			gomega.Expect(k8sClient.Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
			gomega.Eventually(func() *int32 {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
				return finalQueueWorkload.Spec.Priority
			}, util.Timeout, util.Interval).Should(gomega.Equal(&updatedPriority))
		})

		ginkgo.It("admission should not be updated once set", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, workload, testing.MakeAdmission("cluster-queue").Obj())).Should(gomega.Succeed())

			ginkgo.By("Updating the workload setting admission")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission = testing.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj()
				return k8sClient.Status().Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(testing.BeAPIError(testing.ForbiddenError))
		})

		ginkgo.It("reclaimable pod count can change up", func() {
			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*testing.MakePodSet("ps1", 3).Obj(),
					*testing.MakePodSet("ps2", 3).Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
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
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
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
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeAPIError(testing.ForbiddenError))
		})

		ginkgo.It("podSetUpdates should be immutable when state is ready", func() {
			ginkgo.By("Creating a new Workload")
			wl := testing.MakeWorkload(workloadName, ns.Name).
				PodSets(
					*testing.MakePodSet("first", 1).Obj(),
					*testing.MakePodSet("second", 1).Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

			ginkgo.By("Setting admission check state")
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:               "ac1",
					Message:            "old",
					LastTransitionTime: metav1.NewTime(time.Now()),
					PodSetUpdates:      []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
					State:              kueue.CheckStateReady,
				})
				return k8sClient.Status().Update(ctx, wl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating admission check state")
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:               "ac1",
					Message:            "new",
					LastTransitionTime: metav1.NewTime(time.Now()),
					PodSetUpdates:      []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "baz"}}, {Name: "second"}},
					State:              kueue.CheckStateReady,
				})
				return k8sClient.Status().Update(ctx, wl)
			}, util.Timeout, util.Interval).Should(testing.BeAPIError(testing.ForbiddenError))
		})

	})
})
