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

package appwrapper

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadaw "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

const (
	awName                  = "test-aw"
	instanceKey             = "cloud.provider.com/instance"
	priorityClassName       = "test-priority-class"
	priorityValue     int32 = 10
)

var _ = ginkgo.Describe("AppWrapper controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(behavioral.NewNamespaceSelectorExcluding("unmanaged-ns"))))
		unmanagedNamespace := utiltesting.MakeNamespace("unmanaged-ns")
		behavioral.MustCreate(ctx, k8sClient, unmanagedNamespace)

		priorityClass := utiltesting.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		behavioral.MustCreate(ctx, k8sClient, priorityClass)
	})
	ginkgo.AfterAll(func() {
		priorityClass := &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: priorityClassName}}
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "aw-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("basic setup", func() {
		var (
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			onDemandFlavor *kueue.ResourceFlavor
			spotFlavor     *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
					*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()

			behavioral.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, localQueue)
			onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
			behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)
			spotFlavor = utiltestingapi.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
			behavioral.MustCreate(ctx, k8sClient, spotFlavor)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("Should reconcile AppWrappers", func() {
			ginkgo.By("checking the AppWrapper gets suspended when created unsuspended")

			aw := testingaw.MakeAppWrapper(awName, ns.Name).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-0", ns.Name).SetTypeMeta().PriorityClass(priorityClassName).Obj(),
				}).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-1", ns.Name).SetTypeMeta().PriorityClass(priorityClassName).Obj(),
				}).
				Suspend(false).
				Obj()

			behavioral.MustCreate(ctx, k8sClient, aw)
			createdAppWrapper := &awv1beta2.AppWrapper{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: awName, Namespace: ns.Name}, createdAppWrapper)).Should(gomega.Succeed())
				g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the workload is created without queue assigned")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(kueue.LocalQueueName("")), "The Workload shouldn't have .spec.queueName set")
			gomega.Expect(metav1.IsControlledBy(createdWorkload, createdAppWrapper)).To(gomega.BeTrue(), "The Workload should be owned by the AppWrapper")

			ginkgo.By("checking the workload is created with workload priority class", func() {
				behavioral.ExpectWorkloadsWithPodPriority(ctx, k8sClient, priorityClassName, priorityValue, wlLookupKey)
			})

			ginkgo.By("checking the workload is updated with queue name when the AppWrapper does")
			awQueueName := "test-queue"
			createdAppWrapper.Labels = map[string]string{constants.QueueLabel: awQueueName}
			gomega.Expect(k8sClient.Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(kueue.LocalQueueName(awQueueName)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("checking a second non-matching workload is deleted")
			secondWl := &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workloadaw.GetWorkloadNameForAppWrapper("second-workload", "test-uid"),
					Namespace: createdWorkload.Namespace,
				},
				Spec: *createdWorkload.Spec.DeepCopy(),
			}
			gomega.Expect(ctrl.SetControllerReference(createdAppWrapper, secondWl, k8sClient.Scheme())).Should(gomega.Succeed())
			secondWl.Spec.PodSets[0].Count++
			behavioral.MustCreate(ctx, k8sClient, secondWl)
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, key, wl)).Should(utiltesting.BeNotFoundError())
				// check the original wl is still there
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the AppWrapper is unsuspended when workload is assigned")

			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).PodSets(
				kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[0].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
					},
				}, kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[1].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: kueue.ResourceFlavorReference(spotFlavor.Name),
					},
				},
			).Obj()
			integration.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
			integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

			lookupKey := types.NamespacedName{Name: awName, Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
				g.Expect(createdAppWrapper.Spec.Suspend, false).Should(gomega.BeFalse())
				ok, _ := utiltesting.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
				g.Expect(ok).Should(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Expect(createdAppWrapper.Spec.Components[0].PodSetInfos[0].NodeSelector).Should(gomega.Equal(map[string]string{instanceKey: onDemandFlavor.Name}))
			gomega.Expect(createdAppWrapper.Spec.Components[1].PodSetInfos[0].NodeSelector).Should(gomega.Equal(map[string]string{instanceKey: spotFlavor.Name}))
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, createdWorkload)

			ginkgo.By("checking the workload is finished when AppWrapper is completed")
			createdAppWrapper.Status.Phase = awv1beta2.AppWrapperSucceeded
			gomega.Expect(k8sClient.Status().Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
			behavioral.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
		})

		ginkgo.It("An appwrapper created in an unmanaged namespace is not suspended and a workload is not created", func() {
			ginkgo.By("Creating an unsuspended job without a queue-name in unmanaged-ns")

			aw := testingaw.MakeAppWrapper(awName, "unmanaged-ns").
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-0", ns.Name).SetTypeMeta().PriorityClass(priorityClassName).Obj(),
				}).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-1", ns.Name).SetTypeMeta().PriorityClass(priorityClassName).Obj(),
				}).
				Suspend(false).
				Obj()

			behavioral.MustCreate(ctx, k8sClient, aw)
			createdAppWrapper := &awv1beta2.AppWrapper{}
			wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: awName, Namespace: aw.Namespace}, createdAppWrapper)).Should(gomega.Succeed())
				g.Expect(createdAppWrapper.Spec.Suspend, false).Should(gomega.BeFalse())
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should finish the preemption when the appwrapper no longer has resources deployed", framework.SlowSpec, func() {
			aw := testingaw.MakeAppWrapper(awName, ns.Name).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-0", ns.Name).SetTypeMeta().PriorityClass(priorityClassName).Obj(),
				}).
				Suspend(false).
				Queue(localQueue.Name).
				Obj()

			createdWorkload := &kueue.Workload{}
			var wlLookupKey types.NamespacedName

			ginkgo.By("create the appwrapper and admit the workload", func() {
				behavioral.MustCreate(ctx, k8sClient, aw)
				wlLookupKey = types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).PodSets(
					kueue.PodSetAssignment{
						Name: createdWorkload.Spec.PodSets[0].Name,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						},
					},
				).Obj()
				integration.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
				integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("wait for the appwrapper to be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: awName, Namespace: ns.Name}, aw)).Should(gomega.Succeed())
					g.Expect(aw.Spec.Suspend).Should(gomega.BeFalse())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("mark the appwrapper as active with deployed resources", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&aw.Status.Conditions, metav1.Condition{
						Type:   string(awv1beta2.QuotaReserved),
						Status: metav1.ConditionTrue,
						Reason: "SimulatedResourceCreate",
					})
					g.Expect(k8sClient.Status().Update(ctx, aw)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("preempt the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.SetConditionAndUpdate(ctx, k8sClient, createdWorkload, kueue.WorkloadEvicted, metav1.ConditionTrue, kueue.WorkloadEvictedByPreemption, "By test", "evict", behavioral.RealClock)).
						To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for the appwrapper to be suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
					g.Expect(aw.Spec.Suspend).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the workload should stay admitted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("mark the appwrapper as inactive", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&aw.Status.Conditions, metav1.Condition{
						Type:   string(awv1beta2.QuotaReserved),
						Status: metav1.ConditionFalse,
						Reason: "SimulatedResourceDeleted",
					})
					g.Expect(k8sClient.Status().Update(ctx, aw)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the workload should get unadmitted", func() {
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, createdWorkload)
			})
		})
	})

	ginkgo.When("the queue has admission checks", func() {
		var (
			clusterQueueAc *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			testFlavor     *kueue.ResourceFlavor
			jobLookupKey   *types.NamespacedName
			admissionCheck *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			admissionCheck = utiltestingapi.MakeAdmissionCheck("check").ControllerName("ac-controller").Obj()
			behavioral.MustCreate(ctx, k8sClient, admissionCheck)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			clusterQueueAc = utiltestingapi.MakeClusterQueue("prod-cq-with-checks").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("test-flavor").Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks("check").Obj()
			behavioral.MustCreate(ctx, k8sClient, clusterQueueAc)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueueAc.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, localQueue)
			testFlavor = utiltestingapi.MakeResourceFlavor("test-flavor").NodeLabel(instanceKey, "test-flavor").Obj()
			behavioral.MustCreate(ctx, k8sClient, testFlavor)

			jobLookupKey = &types.NamespacedName{Name: awName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, testFlavor, true)
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
		})

		ginkgo.It("labels and annotations should be propagated from admission check to job", func() {
			createdAppWrapper := &awv1beta2.AppWrapper{}
			createdWorkload := &kueue.Workload{}
			aw := testingaw.MakeAppWrapper(awName, ns.Name).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-0", ns.Name).SetTypeMeta().
						Request(corev1.ResourceCPU, "1").
						Obj(),
				}).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-1", ns.Name).SetTypeMeta().
						Request(corev1.ResourceCPU, "1").
						Obj(),
				}).
				Queue("queue").
				Obj()

			ginkgo.By("creating the job", func() {
				behavioral.MustCreate(ctx, k8sClient, aw)
			})

			ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdAppWrapper)).Should(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := &types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}
			ginkgo.By("checking the workload is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add labels & annotations to the admission check in PodSetUpdates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
					workloadpatching.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "test-aw-0",
								Annotations: map[string]string{
									"ann1": "ann-value1",
								},
								Labels: map[string]string{
									"label1": "label-value1",
								},
								NodeSelector: map[string]string{
									"selector1": "selector-value1",
								},
								Tolerations: []corev1.Toleration{
									{
										Key:      "selector1",
										Value:    "selector-value1",
										Operator: corev1.TolerationOpEqual,
										Effect:   corev1.TaintEffectNoSchedule,
									},
								},
							},
							{
								Name: "test-aw-1",
								Annotations: map[string]string{
									"ann1": "ann-value2",
								},
								Labels: map[string]string{
									"label1": "label-value2",
								},
								NodeSelector: map[string]string{
									"selector1": "selector-value2",
								},
							},
						},
					}, behavioral.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("admit the workload", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueAc.Name)).
					PodSets(
						kueue.PodSetAssignment{
							Name: createdWorkload.Spec.PodSets[0].Name,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "test-flavor",
							},
						}, kueue.PodSetAssignment{
							Name: createdWorkload.Spec.PodSets[1].Name,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "test-flavor",
							},
						},
					).
					Obj()
				integration.SetQuotaReservation(ctx, k8sClient, *wlLookupKey, admission)
				integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdAppWrapper)).Should(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeFalse())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the PodSetUpdates are propagated for test-job-0", func() {
				ps1 := createdAppWrapper.Spec.Components[0].PodSetInfos[0]
				gomega.Expect(ps1.Annotations).Should(gomega.HaveKeyWithValue("ann1", "ann-value1"))
				gomega.Expect(ps1.Labels).Should(gomega.HaveKeyWithValue("label1", "label-value1"))
				gomega.Expect(ps1.NodeSelector).Should(gomega.HaveKeyWithValue("selector1", "selector-value1"))
				gomega.Expect(ps1.Tolerations).Should(gomega.BeComparableTo(
					[]corev1.Toleration{
						{
							Key:      "selector1",
							Value:    "selector-value1",
							Operator: corev1.TolerationOpEqual,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
				))
			})

			ginkgo.By("verify the PodSetUpdates are propagated for test-job-1", func() {
				ps2 := createdAppWrapper.Spec.Components[1].PodSetInfos[0]
				gomega.Expect(ps2.NodeSelector).Should(gomega.HaveKeyWithValue("selector1", "selector-value2"))
				gomega.Expect(ps2.Annotations).Should(gomega.HaveKeyWithValue("ann1", "ann-value2"))
				gomega.Expect(ps2.Labels).Should(gomega.HaveKeyWithValue("label1", "label-value2"))
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				integration.SetQuotaReservation(ctx, k8sClient, *wlLookupKey, nil)
				integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdAppWrapper)).Should(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the PodSetInfos are cleared", func() {
				gomega.Expect(createdAppWrapper.Spec.Components[0].PodSetInfos).Should(gomega.BeNil())
				gomega.Expect(createdAppWrapper.Spec.Components[1].PodSetInfos).Should(gomega.BeNil())
			})
		})
	})
})

var _ = ginkgo.Describe("AppWrapper controller for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "aw-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile jobs only when queue is set", framework.SlowSpec, func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		aw := testingaw.MakeAppWrapper(awName, ns.Name).
			Component(testingaw.Component{
				Template: utiltestingjob.MakeJob("job-0", ns.Name).SetTypeMeta().Obj(),
			}).
			Component(testingaw.Component{
				Template: utiltestingjob.MakeJob("job-1", ns.Name).SetTypeMeta().Obj(),
			}).
			Suspend(false).
			Obj()

		behavioral.MustCreate(ctx, k8sClient, aw)
		lookupKey := types.NamespacedName{Name: awName, Namespace: ns.Name}
		createdAppWrapper := &awv1beta2.AppWrapper{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		if createdAppWrapper.Labels == nil {
			createdAppWrapper.Labels = map[string]string{constants.QueueLabel: jobQueueName}
		} else {
			createdAppWrapper.Labels[constants.QueueLabel] = jobQueueName
		}
		gomega.Expect(k8sClient.Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("AppWrapper controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeAppWrapperStatus *awv1beta2.AppWrapperStatus
		beforeCondition        *metav1.Condition
		appWrapperStatus       awv1beta2.AppWrapperStatus
		suspended              bool
		wantCondition          *metav1.Condition
	}

	var defaultFlavor = utiltestingapi.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{}), jobframework.WithCache(schdcache.New(k8sClient))))

		ginkgo.By("Create a resource flavor")
		behavioral.MustCreate(ctx, k8sClient, defaultFlavor)
	})

	ginkgo.AfterAll(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "aw-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a job")
			awQueueName := "test-queue"
			aw := testingaw.MakeAppWrapper(awName, ns.Name).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-0", ns.Name).SetTypeMeta().Obj(),
				}).
				Component(testingaw.Component{
					Template: utiltestingjob.MakeJob("job-1", ns.Name).SetTypeMeta().Obj(),
				}).
				Queue(awQueueName).
				Obj()

			behavioral.MustCreate(ctx, k8sClient, aw)
			lookupKey := types.NamespacedName{Name: awName, Namespace: ns.Name}
			createdAppWrapper := &awv1beta2.AppWrapper{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the AppWrapper")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the AppWrapper")
			admission := utiltestingapi.MakeAdmission("foo").PodSets(
				kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[0].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				}, kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[1].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				},
			).Obj()
			integration.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
			integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

			ginkgo.By("Await for the AppWrapper to be unsuspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
				g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			if podsReadyTestSpec.beforeAppWrapperStatus != nil {
				ginkgo.By("Update the AppWrapper status to simulate its initial progress towards completion")
				createdAppWrapper.Status = *podsReadyTestSpec.beforeAppWrapperStatus
				gomega.Expect(k8sClient.Status().Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
			}

			if podsReadyTestSpec.beforeCondition != nil {
				ginkgo.By("Update the workload status")
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
					g.Expect(cond).Should(gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Update the AppWrapper status to simulate its progress towards completion")
			createdAppWrapper.Status = podsReadyTestSpec.appWrapperStatus
			gomega.Expect(k8sClient.Status().Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())

			if podsReadyTestSpec.suspended {
				ginkgo.By("Unset admission of the workload to suspend the AppWrapper")
				integration.SetQuotaReservation(ctx, k8sClient, wlLookupKey, nil)
				integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			}

			ginkgo.By("Verify the PodsReady condition is added")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
				g.Expect(cond).Should(gomega.BeComparableTo(podsReadyTestSpec.wantCondition, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		},
		ginkgo.Entry("No progress", podsReadyTestSpec{
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Running AppWrapper", podsReadyTestSpec{
			appWrapperStatus: awv1beta2.AppWrapperStatus{
				Conditions: []metav1.Condition{
					{
						Type:               string(awv1beta2.PodsReady),
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadStarted,
						LastTransitionTime: metav1.NewTime(time.Now()),
					},
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("Running AppWrapper; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
			appWrapperStatus: awv1beta2.AppWrapperStatus{
				Conditions: []metav1.Condition{
					{
						Type:               string(awv1beta2.PodsReady),
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadStarted,
						LastTransitionTime: metav1.NewTime(time.Now()),
					},
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("AppWrapper suspended; PodsReady=True before", podsReadyTestSpec{
			beforeAppWrapperStatus: &awv1beta2.AppWrapperStatus{
				Conditions: []metav1.Condition{
					{
						Type:               string(awv1beta2.PodsReady),
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadStarted,
						LastTransitionTime: metav1.NewTime(time.Now()),
					},
				},
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			suspended: true,
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
	)
})

var _ = ginkgo.Describe("AppWrapper controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(false))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "aw-")

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotUntaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		behavioral.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "1").Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.It("Should schedule AppWrappers as they fit in their ClusterQueue", framework.SlowSpec, func() {
		ginkgo.By("creating localQueue")
		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)

		ginkgo.By("checking a dev job starts")
		aw := testingaw.MakeAppWrapper(awName, ns.Name).
			Component(testingaw.Component{
				Template: utiltestingjob.MakeJob("job-0", ns.Name).SetTypeMeta().
					Request(corev1.ResourceCPU, "1").
					Obj(),
			}).
			Component(testingaw.Component{
				Template: utiltestingjob.MakeJob("job-1", ns.Name).SetTypeMeta().
					Request(corev1.ResourceCPU, "1").
					Parallelism(3).
					Obj(),
			}).
			Queue(localQueue.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, aw)
		createdAppWrapper := &awv1beta2.AppWrapper{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: aw.Name, Namespace: aw.Namespace}, createdAppWrapper)).
				Should(gomega.Succeed())
			g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeFalse())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		gomega.Expect(createdAppWrapper.Spec.Components[0].PodSetInfos[0].NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdAppWrapper.Spec.Components[1].PodSetInfos[0].NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
	})
})

var _ = ginkgo.Describe("AppWrapper controller with TopologyAwareScheduling", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		nodeGroupLabel = "node-group"
		tasBlockLabel  = "cloud.com/topology-block"
	)

	var (
		ns           *corev1.Namespace
		nodes        []corev1.Node
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(true))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-aw-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("b1").
				Label("node-group", "tas").
				Label(tasBlockLabel, "b1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeTopology("default").Levels(tasBlockLabel).Obj()
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
	})

	ginkgo.It("should place grouped PyTorchJob workers by index across topology domains", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)
		ginkgo.By("creating topology domains with capacity for three and two Pods")
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&nodes[0]), &nodes[0])).To(gomega.Succeed())
		nodes[0].Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("3")
		gomega.Expect(k8sClient.Status().Update(ctx, &nodes[0])).To(gomega.Succeed())
		nodes = append(nodes, *testingnode.MakeNode("b2").
			Label(nodeGroupLabel, "tas").
			Label(tasBlockLabel, "b2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().Obj())
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes[1:])

		ginkgo.By("admitting an AppWrapper with grouped PyTorchJob replicas")
		pytorchJob := testingpytorchjob.MakePyTorchJob("pytorch", ns.Name).
			PyTorchReplicaSpecsDefault().Parallelism(4).
			Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "1").
			Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "1").
			PodAnnotation(kftraining.PyTorchJobReplicaTypeMaster, kueue.PodSetGroupName, "training").
			PodAnnotation(kftraining.PyTorchJobReplicaTypeWorker, kueue.PodSetGroupName, "training").
			PodAnnotation(kftraining.PyTorchJobReplicaTypeMaster, kueue.PodSetPreferredTopologyAnnotation, tasBlockLabel).
			PodAnnotation(kftraining.PyTorchJobReplicaTypeWorker, kueue.PodSetPreferredTopologyAnnotation, tasBlockLabel).
			SetTypeMeta().Obj()
		aw := testingaw.MakeAppWrapper(awName, ns.Name).
			Component(testingaw.Component{Template: pytorchJob}).
			Queue(localQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, aw)
		wl := &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID),
				Namespace: ns.Name,
			},
		}
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
			g.Expect(aw.Spec.Suspend).To(gomega.BeFalse())
			g.Expect(aw.Spec.Components[0].PodSetInfos).To(gomega.HaveLen(2))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(2))

		ginkgo.By("checking the Master Pod's topology placement")
		masterBlocks := topologyAssignmentBlocks(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
		masterTemplate := pytorchJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.DeepCopy()
		mergeAppWrapperPodSetInfo(masterTemplate, aw.Spec.Components[0].PodSetInfos[0])
		masterPod := &corev1.Pod{
			ObjectMeta: *masterTemplate.ObjectMeta.DeepCopy(),
			Spec:       *masterTemplate.Spec.DeepCopy(),
		}
		masterPod.Name = "replica-0-0"
		masterPod.Namespace = ns.Name
		masterPod.Labels[kftraining.ReplicaIndexLabel] = "0"
		masterPod.Spec.TerminationGracePeriodSeconds = new(int64(0))
		behavioral.MustCreate(ctx, k8sClient, masterPod)
		expectPodTopologyPlacement(masterPod, map[string]string{nodeGroupLabel: "tas", tasBlockLabel: masterBlocks[0]})

		ginkgo.By("checking Worker Pods follow their indexes despite out-of-order creation")
		workerAssignment := wl.Status.Admission.PodSetAssignments[1].TopologyAssignment
		workerBlocks := topologyAssignmentBlocks(workerAssignment)
		workerTemplate := pytorchJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.DeepCopy()
		mergeAppWrapperPodSetInfo(workerTemplate, aw.Spec.Components[0].PodSetInfos[1])
		for _, index := range outOfOrderPodIndexes(workerAssignment) {
			pod := &corev1.Pod{
				ObjectMeta: *workerTemplate.ObjectMeta.DeepCopy(),
				Spec:       *workerTemplate.Spec.DeepCopy(),
			}
			pod.Name = fmt.Sprintf("replica-1-%d", index)
			pod.Namespace = ns.Name
			pod.Labels[kftraining.ReplicaIndexLabel] = strconv.Itoa(index)
			pod.Spec.TerminationGracePeriodSeconds = new(int64(0))
			behavioral.MustCreate(ctx, k8sClient, pod)
			expectPodTopologyPlacement(pod, map[string]string{nodeGroupLabel: "tas", tasBlockLabel: workerBlocks[index]})
		}
	})

	ginkgo.It("should place grouped JobSet pods by index across topology domains", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)
		ginkgo.By("creating topology domains with capacity for three and two Pods")
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&nodes[0]), &nodes[0])).To(gomega.Succeed())
		nodes[0].Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("3")
		gomega.Expect(k8sClient.Status().Update(ctx, &nodes[0])).To(gomega.Succeed())
		nodes = append(nodes, *testingnode.MakeNode("b2").
			Label(nodeGroupLabel, "tas").
			Label(tasBlockLabel, "b2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().Obj())
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes[1:])

		ginkgo.By("admitting an AppWrapper with grouped JobSet replicated jobs")
		jobSet := testingjobset.MakeJobSet("jobset", ns.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name: "leader", Replicas: 1, Parallelism: 1, Completions: 1,
					PodAnnotations: map[string]string{
						kueue.PodSetGroupName:                   "training",
						kueue.PodSetPreferredTopologyAnnotation: tasBlockLabel,
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name: "worker", Replicas: 2, Parallelism: 2, Completions: 2,
					PodAnnotations: map[string]string{
						kueue.PodSetGroupName:                   "training",
						kueue.PodSetPreferredTopologyAnnotation: tasBlockLabel,
					},
				},
			).
			Request("leader", corev1.ResourceCPU, "1").
			Request("worker", corev1.ResourceCPU, "1").
			SetTypeMeta().Obj()
		for i := range jobSet.Spec.ReplicatedJobs {
			jobSet.Spec.ReplicatedJobs[i].Template.Spec.CompletionMode = new(batchv1.IndexedCompletion)
		}
		aw := testingaw.MakeAppWrapper(awName, ns.Name).
			Component(testingaw.Component{Template: jobSet}).
			Queue(localQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, aw)
		wl := &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID),
				Namespace: ns.Name,
			},
		}
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
			g.Expect(aw.Spec.Suspend).To(gomega.BeFalse())
			g.Expect(aw.Spec.Components[0].PodSetInfos).To(gomega.HaveLen(2))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(2))

		ginkgo.By("checking the leader Pod's topology placement")
		leaderJob := &jobSet.Spec.ReplicatedJobs[0]
		leaderBlocks := topologyAssignmentBlocks(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
		leaderTemplate := leaderJob.Template.Spec.Template.DeepCopy()
		mergeAppWrapperPodSetInfo(leaderTemplate, aw.Spec.Components[0].PodSetInfos[0])
		leaderPod := &corev1.Pod{
			ObjectMeta: *leaderTemplate.ObjectMeta.DeepCopy(),
			Spec:       *leaderTemplate.Spec.DeepCopy(),
		}
		leaderPod.Name = "replica-0-0"
		leaderPod.Namespace = ns.Name
		leaderPod.Labels[batchv1.JobCompletionIndexAnnotation] = "0"
		leaderPod.Labels[jobsetapi.JobIndexKey] = "0"
		leaderPod.Spec.TerminationGracePeriodSeconds = new(int64(0))
		behavioral.MustCreate(ctx, k8sClient, leaderPod)
		expectPodTopologyPlacement(leaderPod, map[string]string{nodeGroupLabel: "tas", tasBlockLabel: leaderBlocks[0]})

		ginkgo.By("checking worker Pods follow their indexes despite out-of-order creation")
		workerJob := &jobSet.Spec.ReplicatedJobs[1]
		workerAssignment := wl.Status.Admission.PodSetAssignments[1].TopologyAssignment
		workerBlocks := topologyAssignmentBlocks(workerAssignment)
		workerTemplate := workerJob.Template.Spec.Template.DeepCopy()
		mergeAppWrapperPodSetInfo(workerTemplate, aw.Spec.Components[0].PodSetInfos[1])
		for _, index := range outOfOrderPodIndexes(workerAssignment) {
			pod := &corev1.Pod{
				ObjectMeta: *workerTemplate.ObjectMeta.DeepCopy(),
				Spec:       *workerTemplate.Spec.DeepCopy(),
			}
			pod.Name = fmt.Sprintf("replica-1-%d", index)
			pod.Namespace = ns.Name
			pod.Labels[batchv1.JobCompletionIndexAnnotation] = strconv.Itoa(index % int(*workerJob.Template.Spec.Parallelism))
			pod.Labels[jobsetapi.JobIndexKey] = strconv.Itoa(index / int(*workerJob.Template.Spec.Parallelism))
			pod.Spec.TerminationGracePeriodSeconds = new(int64(0))
			behavioral.MustCreate(ctx, k8sClient, pod)
			expectPodTopologyPlacement(pod, map[string]string{nodeGroupLabel: "tas", tasBlockLabel: workerBlocks[index]})
		}
	})

	ginkgo.It("should admit workload which fits in a required topology domain", framework.SlowSpec, func() {
		aw := testingaw.MakeAppWrapper(awName, ns.Name).
			Component(testingaw.Component{
				Template: utiltestingjob.MakeJob("job", ns.Name).
					PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, tasBlockLabel).
					Request(corev1.ResourceCPU, "1").
					SetTypeMeta().
					Obj(),
			}).
			Queue(localQueue.Name).
			Suspend(false).
			Obj()
		ginkgo.By("creating a job which requires block", func() {
			behavioral.MustCreate(ctx, k8sClient, aw)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{{
					Name:  wl.Spec.PodSets[0].Name,
					Count: 1,
					TopologyRequest: &kueue.PodSetTopologyRequest{
						Required:      new(tasBlockLabel),
						PodIndexLabel: new(batchv1.JobCompletionIndexAnnotation),
					},
				}}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
		})

		ginkgo.By("verify admission for the workload", func() {
			wl := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels:  []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{{Count: 1, Values: []string{"b1"}}},
					}),
				))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})

func topologyAssignmentBlocks(assignment *kueue.TopologyAssignment) []string {
	ginkgo.GinkgoHelper()
	gomega.Expect(assignment).NotTo(gomega.BeNil())
	var blocks []string
	for domain := range tas.InternalSeqFrom(assignment) {
		for range domain.Count {
			blocks = append(blocks, domain.Values[0])
		}
	}
	return blocks
}

func outOfOrderPodIndexes(assignment *kueue.TopologyAssignment) []int {
	ginkgo.GinkgoHelper()
	domains := slices.Collect(tas.InternalSeqFrom(assignment))
	gomega.Expect(domains).To(gomega.HaveLen(2))
	// Start in the second domain so greedy placement cannot pass this check.
	firstIndex := int(domains[0].Count)
	indexes := []int{firstIndex}
	for i := int(domains[0].Count+domains[1].Count) - 1; i >= 0; i-- {
		if i != firstIndex {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func mergeAppWrapperPodSetInfo(template *corev1.PodTemplateSpec, info awv1beta2.AppWrapperPodSetInfo) {
	ginkgo.GinkgoHelper()
	gomega.Expect(podset.Merge(ctrl.LoggerFrom(ctx), &template.ObjectMeta, &template.Spec, podset.PodSetInfo{
		Annotations:     info.Annotations,
		Labels:          info.Labels,
		NodeSelector:    info.NodeSelector,
		Tolerations:     info.Tolerations,
		SchedulingGates: info.SchedulingGates,
	})).To(gomega.Succeed())
}

func expectPodTopologyPlacement(pod *corev1.Pod, nodeSelector map[string]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
		g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
		g.Expect(pod.Spec.NodeSelector).To(gomega.Equal(nodeSelector))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}
