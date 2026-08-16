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

package multikueue

import (
	"context"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue Pod", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		f = setupMultiKueueFixture()
	})

	ginkgo.AfterEach(func() {
		f.teardown()
	})

	ginkgo.It("Should create a pod on worker if admitted", func() {
		pod := testingpod.MakePod("pod1", f.managerNs.Name).
			Queue(f.managerLq.Name).
			ManagedByKueueLabel().
			KueueSchedulingGate().
			Obj()

		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, pod)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: f.managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker1"`,
			)

			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker pod", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := corev1.Pod{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(pod), &createdPod)).To(gomega.Succeed())
				createdPod.Status.Phase = corev1.PodSucceeded
				createdPod.Status.Conditions = append(createdPod.Status.Conditions,
					corev1.PodCondition{
						Type:               corev1.PodReadyToStartContainers,
						Status:             corev1.ConditionFalse,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             "",
					},
					corev1.PodCondition{
						Type:               corev1.PodInitialized,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             string(corev1.PodSucceeded),
					},
					corev1.PodCondition{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             string(corev1.PodSucceeded),
					},
					corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             string(corev1.PodSucceeded),
					},
					corev1.PodCondition{
						Type:               corev1.PodScheduled,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             "",
					},
				)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdPod)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, "")
		})
	})

	ginkgo.It("Should create a pod group on worker if admitted", func() {
		groupName := "test-group"
		podgroup := testingpod.MakePod(groupName, f.managerNs.Name).
			Queue(f.managerLq.Name).
			ManagedByKueueLabel().
			KueueFinalizer().
			KueueSchedulingGate().
			MakeGroup(3)

		for _, p := range podgroup {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, p)).Should(gomega.Succeed())
		}

		// any pod should give the same workload Key
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: groupName, Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(
				utiltestingapi.MakePodSetAssignment("bf90803c").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(3).Obj(),
			).Obj()
		ginkgo.By("setting workload reservation in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(3)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker1"`,
			)

			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		pods := corev1.PodList{}
		gomega.Expect(managerTestCluster.client.List(managerTestCluster.ctx, &pods)).To(gomega.Succeed())

		ginkgo.By("finishing the worker pod", func() {
			pods := corev1.PodList{}
			gomega.Expect(worker1TestCluster.client.List(worker1TestCluster.ctx, &pods)).To(gomega.Succeed())
			for _, p := range podgroup {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := corev1.Pod{}
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(p), &createdPod)).To(gomega.Succeed())
					createdPod.Status.Phase = corev1.PodSucceeded
					createdPod.Status.Conditions = append(createdPod.Status.Conditions,
						corev1.PodCondition{
							Type:   corev1.PodReadyToStartContainers,
							Status: corev1.ConditionFalse,
							Reason: "",
						},
						corev1.PodCondition{
							Type:   corev1.PodInitialized,
							Status: corev1.ConditionTrue,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.ContainersReady,
							Status: corev1.ConditionFalse,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
							Reason: "",
						},
					)
					g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdPod)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}
			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, "Pods succeeded: 3/3.")
		})
	})

	ginkgo.It("Should handle a pod group admission with extra non-multikueue admission checks defined", func() {
		var testAc, testAc2 *kueue.AdmissionCheck

		ginkgo.By("creating non-multikueue ACs and adding them to the CQ", func() {
			testAc = utiltestingapi.MakeAdmissionCheck("test-ac").
				ControllerName("test-controller").
				Active(metav1.ConditionTrue).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, testAc)

			testAc2 = utiltestingapi.MakeAdmissionCheck("test-ac-2").
				ControllerName("test-controller-2").
				Active(metav1.ConditionTrue).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, testAc2)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerCq), f.managerCq)).To(gomega.Succeed())
				f.managerCq.Spec.AdmissionChecksStrategy.AdmissionChecks = append(
					f.managerCq.Spec.AdmissionChecksStrategy.AdmissionChecks,
					kueue.AdmissionCheckStrategyRule{Name: kueue.AdmissionCheckReference(testAc.Name)},
					kueue.AdmissionCheckStrategyRule{Name: kueue.AdmissionCheckReference(testAc2.Name)},
				)
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, f.managerCq)).To(gomega.Succeed())
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		groupName := "test-group"
		podgroup := testingpod.MakePod(groupName, f.managerNs.Name).
			Queue(f.managerLq.Name).
			ManagedByKueueLabel().
			KueueFinalizer().
			KueueSchedulingGate().
			MakeGroup(3)

		for _, p := range podgroup {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, p)).Should(gomega.Succeed())
		}

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: groupName, Namespace: f.managerNs.Name}

		ginkgo.By("checking workload in manager is set up correctly", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(f.multiKueueAC.Name))).
					To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(f.multiKueueAC.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))
				g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc.Name))).
					To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(testAc.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))
				g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc2.Name))).
					To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(testAc2.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(
				utiltestingapi.MakePodSetAssignment("bf90803c").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(3).Obj(),
			).Obj()
		ginkgo.By("setting workload reservation in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(3)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters didn't happen due to pending non-multikueue ACs", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Update first pending AC to Ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, updatedWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(updatedWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				acs.State = kueue.CheckStateReady
				acs.Message = "Test AC is ready"
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, updatedWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters still didn't happen due to second pending AC", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Update second pending AC to Ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, updatedWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(updatedWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc2.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				acs.State = kueue.CheckStateReady
				acs.Message = "Test AC 2 is ready"
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, updatedWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(f.multiKueueAC.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(acs.Message).To(gomega.Equal(`The workload was admitted on "worker1"`))
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		pods := corev1.PodList{}
		gomega.Expect(managerTestCluster.client.List(managerTestCluster.ctx, &pods)).To(gomega.Succeed())

		ginkgo.By("finishing the worker pod", func() {
			pods := corev1.PodList{}
			gomega.Expect(worker1TestCluster.client.List(worker1TestCluster.ctx, &pods)).To(gomega.Succeed())
			for _, p := range podgroup {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := corev1.Pod{}
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(p), &createdPod)).To(gomega.Succeed())
					createdPod.Status.Phase = corev1.PodSucceeded
					createdPod.Status.Conditions = append(createdPod.Status.Conditions,
						corev1.PodCondition{
							Type:   corev1.PodReadyToStartContainers,
							Status: corev1.ConditionFalse,
							Reason: "",
						},
						corev1.PodCondition{
							Type:   corev1.PodInitialized,
							Status: corev1.ConditionTrue,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.ContainersReady,
							Status: corev1.ConditionFalse,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
							Reason: "",
						},
					)
					g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdPod)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}
			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, "Pods succeeded: 3/3.")
		})
	})
})
