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

package capacitybuffer

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/capacitybuffer/autoscaling.x-k8s.io/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/capacitybuffer"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

const (
	resourceGPU corev1.ResourceName = "example.com/gpu"
)

// createGatedPods stands in for a Job controller, which this suite does not run.
func createGatedPods(wl *kueue.Workload, podSet string, first, count int) []*corev1.Pod {
	ginkgo.GinkgoHelper()
	pods := make([]*corev1.Pod, 0, count)
	for i := first; i < first+count; i++ {
		pod := testingpod.MakePod(fmt.Sprintf("%s-%s-%d", wl.Name, podSet, i), wl.Namespace).
			Annotation(kueue.WorkloadAnnotation, wl.Name).
			Annotation(kueuealpha.CapacityBufferGatedAnnotation, "true").
			Label(constants.PodSetLabel, podSet).
			Gate(kueuealpha.CapacityBufferSchedulingGate).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, pod)
		pods = append(pods, pod)
	}
	return pods
}

func expectPodsGated(pods []*corev1.Pod, gated bool) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		for _, pod := range pods {
			got := &corev1.Pod{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), got)).To(gomega.Succeed())
			g.Expect(utilpod.HasGate(got, kueuealpha.CapacityBufferSchedulingGate)).To(gomega.Equal(gated), "pod %s", pod.Name)
		}
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

// setCapacityBufferFitsExistingCapacity simulates the autoscaler reporting that
// the capacity of the buffer is available.
func setCapacityBufferFitsExistingCapacity(key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		cb := &autoscaling.CapacityBuffer{}
		g.Expect(k8sClient.Get(ctx, key, cb)).To(gomega.Succeed())
		apimeta.SetStatusCondition(&cb.Status.Conditions, metav1.Condition{
			Type:    capacitybuffer.ProvisioningCondition,
			Status:  metav1.ConditionTrue,
			Reason:  capacitybuffer.ReasonFitsExistingCapacity,
			Message: "All buffer pods fit existing capacity",
		})
		g.Expect(k8sClient.Status().Update(ctx, cb)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("CapacityBuffer", ginkgo.Label("controller:capacitybuffer", "area:admissionchecks", "feature:capacitybuffer"), func() {
	var (
		ns        *corev1.Namespace
		cbc       *kueuealpha.CapacityBufferConfig
		ac        *kueue.AdmissionCheck
		rf        *kueue.ResourceFlavor
		cq        *kueue.ClusterQueue
		lq        *kueue.LocalQueue
		wl        *kueue.Workload
		wlKey     types.NamespacedName
		bufferKey types.NamespacedName
		loaderKey types.NamespacedName
		admission *kueue.Admission
		// createConfig is false for specs that create the config themselves.
		createConfig bool
	)

	ginkgo.BeforeEach(func() {
		// The Pod index used to find the Pods of a PodSet is registered with the gate.
		// TAS and elastic jobs register it too, so they are disabled to cover the CapacityBufferACC case.
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CapacityBufferACC, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, false)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, false)
	})

	ginkgo.JustBeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterEach(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("A workload is using a CapacityBuffer admission check", func() {
		ginkgo.BeforeEach(func() {
			cbc = &kueuealpha.CapacityBufferConfig{
				Name: "cb-config",
				Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{resourceGPU}},
			}
			createConfig = true
		})

		ginkgo.JustBeforeEach(func() {
			ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "capacity-buffer-")
			if createConfig {
				behavioral.MustCreate(ctx, k8sClient, cbc)
			}

			ac = utiltestingapi.MakeAdmissionCheck("ac-capacity-buffer").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", cbc.Name).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ac)

			rf = utiltestingapi.MakeResourceFlavor("gpu-flavor").NodeLabel("pool", "gpu").Obj()
			behavioral.MustCreate(ctx, k8sClient, rf)

			cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(resourceGPU, "8").
					Resource(corev1.ResourceCPU, "8").
					Obj()).
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)

			// Without the config, the admission check and the queues stay inactive.
			if createConfig {
				behavioral.ExpectAdmissionChecksToBeActive(ctx, k8sClient, ac)
				behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
				behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
			}

			wl = utiltestingapi.MakeWorkload("wl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(
					*utiltestingapi.MakePodSet("trainer", 2).
						Request(resourceGPU, "1").
						Limit(resourceGPU, "1").
						Image("image").
						Obj(),
					*utiltestingapi.MakePodSet("loader", 1).
						Request(corev1.ResourceCPU, "1").
						Image("image").
						Obj(),
				).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			wlKey = client.ObjectKeyFromObject(wl)
			bufferKey = types.NamespacedName{
				Namespace: ns.Name,
				Name:      capacitybuffer.CapacityBufferName(wl.Name, kueue.AdmissionCheckReference(ac.Name), "trainer"),
			}
			loaderKey = types.NamespacedName{
				Namespace: ns.Name,
				Name:      capacitybuffer.CapacityBufferName(wl.Name, kueue.AdmissionCheckReference(ac.Name), "loader"),
			}

			admission = utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).
				PodSets(
					utiltestingapi.MakePodSetAssignment("trainer").
						Assignment(resourceGPU, kueue.ResourceFlavorReference(rf.Name), "2").
						Count(2).
						Obj(),
					utiltestingapi.MakePodSetAssignment("loader").
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "1").
						Count(1).
						Obj(),
				).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cbc, true)
		})

		ginkgo.It("Should not create CapacityBuffers before quota is reserved", framework.SlowSpec, func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit the workload once the buffers fit existing capacity and delete them once the Pods exist", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking that a CapacityBuffer is created only for the podset requesting managed resources", func() {
				createdBuffer := &autoscaling.CapacityBuffer{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, bufferKey, createdBuffer)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(createdBuffer.Spec.ProvisioningStrategy).To(gomega.Equal(new(capacitybuffer.ProvisioningStrategy)))
				gomega.Expect(createdBuffer.Spec.Replicas).To(gomega.Equal(new(int32(2))))
				gomega.Expect(createdBuffer.Spec.PodTemplateRef).To(gomega.Equal(&autoscaling.LocalObjectRef{Name: bufferKey.Name}))
				gomega.Expect(k8sClient.Get(ctx, loaderKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())

				createdTemplate := &corev1.PodTemplate{}
				gomega.Expect(k8sClient.Get(ctx, bufferKey, createdTemplate)).To(gomega.Succeed())
				gomega.Expect(createdTemplate.Template.Spec.NodeSelector).To(gomega.Equal(map[string]string{"pool": "gpu"}))
			})

			ginkgo.By("Checking that the admission check is pending", func() {
				behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStatePending)
			})

			ginkgo.By("Setting the CapacityBuffer as fitting existing capacity", func() {
				setCapacityBufferFitsExistingCapacity(bufferKey)
			})

			ginkgo.By("Checking that the admission check is ready and asks to gate the podset with a buffer", func() {
				behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStateReady)
				updatedWl := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
				gomega.Expect(updatedWl.Status.AdmissionChecks[0].PodSetUpdates).To(gomega.Equal([]kueue.PodSetUpdate{{
					Name:        "trainer",
					Annotations: map[string]string{kueuealpha.CapacityBufferGatedAnnotation: "true"},
				}}))
			})

			ginkgo.By("Admitting the workload", func() {
				integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			var pods []*corev1.Pod
			ginkgo.By("Creating one of the two Pods of the podset", func() {
				pods = createGatedPods(wl, "trainer", 0, 1)
			})

			ginkgo.By("Checking that the CapacityBuffer is kept and the Pod stays gated", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).To(gomega.Succeed())
				}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				expectPodsGated(pods, true)
			})

			ginkgo.By("Creating the second Pod of the podset", func() {
				pods = append(pods, createGatedPods(wl, "trainer", 1, 1)...)
			})

			ginkgo.By("Checking that the CapacityBuffer and its PodTemplate are deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
					g.Expect(k8sClient.Get(ctx, bufferKey, &corev1.PodTemplate{})).Should(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Pods are ungated", func() {
				expectPodsGated(pods, false)
			})
		})

		ginkgo.When("Every podset has a buffer", func() {
			ginkgo.BeforeEach(func() {
				cbc.Spec.ManagedResources = nil
			})

			ginkgo.It("Should release the buffer of each podset once its Pods exist", func() {
				ginkgo.By("Admitting the workload once both buffers fit existing capacity", func() {
					integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
					setCapacityBufferFitsExistingCapacity(bufferKey)
					setCapacityBufferFitsExistingCapacity(loaderKey)
					behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStateReady)
					integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				})

				// Pods are created one podset at a time, as by a JobSet with an
				// InOrder startup policy.
				var loaderPods []*corev1.Pod
				ginkgo.By("Creating the Pods of the first podset", func() {
					loaderPods = createGatedPods(wl, "loader", 0, 1)
				})

				ginkgo.By("Checking that only the buffer of the first podset is deleted and its Pods are ungated", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, loaderKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					expectPodsGated(loaderPods, false)
					gomega.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).To(gomega.Succeed())
				})

				var trainerPods []*corev1.Pod
				ginkgo.By("Creating the Pods of the second podset", func() {
					trainerPods = createGatedPods(wl, "trainer", 0, 2)
				})

				ginkgo.By("Checking that the buffer of the second podset is deleted and its Pods are ungated", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					expectPodsGated(trainerPods, false)
				})
			})

			ginkgo.It("Should release the remaining buffers of an admitted workload after its config is deleted", func() {
				ginkgo.By("Admitting the workload once both buffers fit existing capacity", func() {
					integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
					setCapacityBufferFitsExistingCapacity(bufferKey)
					setCapacityBufferFitsExistingCapacity(loaderKey)
					behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStateReady)
					integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				})

				ginkgo.By("Releasing the buffer of the first podset", func() {
					loaderPods := createGatedPods(wl, "loader", 0, 1)
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, loaderKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					expectPodsGated(loaderPods, false)
				})

				ginkgo.By("Deleting the config while the second podset waits for its Pods", func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cbc, true)
					gomega.Eventually(func(g gomega.Gomega) {
						got := &kueue.AdmissionCheck{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), got)).To(gomega.Succeed())
						g.Expect(apimeta.IsStatusConditionFalse(got.Status.Conditions, kueue.AdmissionCheckActive)).To(gomega.BeTrue())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				var trainerPods []*corev1.Pod
				ginkgo.By("Creating the Pods of the second podset", func() {
					trainerPods = createGatedPods(wl, "trainer", 0, 2)
				})

				ginkgo.By("Checking that the buffer of the second podset is deleted and its Pods are ungated", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					expectPodsGated(trainerPods, false)
				})

				ginkgo.By("Checking that the admission check stays ready and the workload stays admitted", func() {
					behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStateReady)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				})
			})
		})

		ginkgo.It("Should delete the buffers when the quota is released and recreate them on the next reservation", func() {
			var firstUID types.UID
			ginkgo.By("Setting the quota reservation to the workload", func() {
				integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
				gomega.Eventually(func(g gomega.Gomega) {
					cb := &autoscaling.CapacityBuffer{}
					g.Expect(k8sClient.Get(ctx, bufferKey, cb)).To(gomega.Succeed())
					firstUID = cb.UID
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Releasing the quota reservation", func() {
				integration.SetQuotaReservation(ctx, k8sClient, wlKey, nil)
			})

			ginkgo.By("Checking that the CapacityBuffer is deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Reserving the quota again", func() {
				integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking that a new CapacityBuffer is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					cb := &autoscaling.CapacityBuffer{}
					g.Expect(k8sClient.Get(ctx, bufferKey, cb)).To(gomega.Succeed())
					g.Expect(cb.UID).NotTo(gomega.Equal(firstUID))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("The config has a timeout", func() {
			ginkgo.BeforeEach(func() {
				cbc.Spec.TimeoutSeconds = new(int32(1))
			})

			ginkgo.It("Should evict the workload when the buffers do not fit existing capacity in time", func() {
				ginkgo.By("Setting the quota reservation to the workload", func() {
					integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
				})

				ginkgo.By("Checking that the workload is evicted by the admission check", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
						_, evicted := workloadevict.IsEvictedByAdmissionCheck(updatedWl)
						g.Expect(evicted).To(gomega.BeTrue())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking that the CapacityBuffer is deleted and the check is reset to pending", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStatePending)
				})
			})

			ginkgo.It("Should evict the workload when its buffers cannot be created in time", func() {
				foreign := &autoscaling.CapacityBuffer{
					Name:      bufferKey.Name,
					Namespace: bufferKey.Namespace,
					Spec: autoscaling.CapacityBufferSpec{
						PodTemplateRef: &autoscaling.LocalObjectRef{Name: "other"},
						Replicas:       new(int32(1)),
					},
				}
				ginkgo.By("Creating a CapacityBuffer with the buffer's name that the workload does not own", func() {
					behavioral.MustCreate(ctx, k8sClient, foreign)
				})

				ginkgo.By("Setting the quota reservation to the workload", func() {
					integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
				})

				ginkgo.By("Checking that the workload is evicted by the admission check", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
						_, evicted := workloadevict.IsEvictedByAdmissionCheck(updatedWl)
						g.Expect(evicted).To(gomega.BeTrue())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking that the foreign CapacityBuffer is kept", func() {
					got := &autoscaling.CapacityBuffer{}
					gomega.Expect(k8sClient.Get(ctx, bufferKey, got)).To(gomega.Succeed())
					gomega.Expect(got.UID).To(gomega.Equal(foreign.UID))
				})
			})
		})

		ginkgo.When("No podset requests a managed resource", func() {
			ginkgo.BeforeEach(func() {
				cbc.Spec.ManagedResources = []corev1.ResourceName{"example.com/tpu"}
			})

			ginkgo.It("Should set the admission check to ready without creating buffers", func() {
				ginkgo.By("Setting the quota reservation to the workload", func() {
					integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
				})

				ginkgo.By("Checking that the admission check is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
						g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.HaveLen(1))
						g.Expect(updatedWl.Status.AdmissionChecks[0].State).To(gomega.Equal(kueue.CheckStateReady))
						g.Expect(updatedWl.Status.AdmissionChecks[0].Message).To(gomega.Equal(capacitybuffer.NoBufferNeededMessage))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking that no CapacityBuffer is created", func() {
					gomega.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
					gomega.Expect(k8sClient.Get(ctx, loaderKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
				})

				ginkgo.By("Checking that the workload is admitted", func() {
					integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				})
			})
		})

		ginkgo.When("The config is created after the workload reserves quota", func() {
			ginkgo.BeforeEach(func() {
				createConfig = false
			})

			ginkgo.It("Should create the buffers once the config exists", func() {
				ginkgo.By("Setting the quota reservation to the workload", func() {
					integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
				})

				ginkgo.By("Checking that the admission check waits for its config", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
						g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.HaveLen(1))
						g.Expect(updatedWl.Status.AdmissionChecks[0].State).To(gomega.Equal(kueue.CheckStatePending))
						g.Expect(updatedWl.Status.AdmissionChecks[0].Message).To(gomega.Equal(capacitybuffer.CheckInactiveMessage))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					gomega.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
				})

				ginkgo.By("Creating the config", func() {
					behavioral.MustCreate(ctx, k8sClient, cbc)
				})

				ginkgo.By("Checking that the CapacityBuffer is created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, bufferKey, &autoscaling.CapacityBuffer{})).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStatePending)
				})
			})
		})
	})

	ginkgo.When("The CapacityBufferConfig is created after the admission check", func() {
		ginkgo.JustBeforeEach(func() {
			cbc = &kueuealpha.CapacityBufferConfig{Name: "late-config"}
			ac = utiltestingapi.MakeAdmissionCheck("ac-late-config").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", cbc.Name).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ac)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cbc, true)
		})

		ginkgo.It("Should activate the admission check once the config exists", func() {
			ginkgo.By("Checking that the admission check is inactive", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					updatedAc := &kueue.AdmissionCheck{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), updatedAc)).To(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionFalse(updatedAc.Status.Conditions, kueue.AdmissionCheckActive)).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Creating the config", func() {
				behavioral.MustCreate(ctx, k8sClient, cbc)
			})

			ginkgo.By("Checking that the admission check becomes active", func() {
				behavioral.ExpectAdmissionChecksToBeActive(ctx, k8sClient, ac)
			})
		})
	})
})
