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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	workloadrayservice "sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("MultiKueue Kuberay", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce,
				multikueue.WithWorkerLostTimeout(time.Minute))
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

	ginkgo.It("Should run a RayJob on worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		rayjob := testingrayjob.MakeJob("rayjob1", f.managerNs.Name).
			WithSubmissionMode(rayv1.InteractiveMode).
			Queue(f.managerLq.Name).
			WithHistoryServerOptions(&rayv1.HistoryServerOptions{
				CollectorOptions: &rayv1.CollectorOptions{
					Image: new("quay.io/kuberay/collector:v1.7.0"),
				},
			}).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: f.managerNs.Name}
		behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("propagating history server options to the worker RayJob", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.RayClusterSpec.HistoryServerOptions).To(gomega.Equal(rayjob.Spec.RayClusterSpec.HistoryServerOptions))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("changing the status of the RayJob in the worker, updates the manager's RayJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusRunning
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker RayJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := ""
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				createdRayJob.Status.JobStatus = rayv1.JobStatusSucceeded
				createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusComplete
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should reverse sync an autoscaling RayJob when its child RayCluster changes", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueRayInTreeAutoscaling, true)

		admission := func(workerCount int32) *utiltestingapi.AdmissionWrapper {
			return utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
				utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
				utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(workerCount).Obj(),
			)
		}
		getWorkload := func(g gomega.Gomega, clnt client.Client, key types.NamespacedName) *kueue.Workload {
			ginkgo.GinkgoHelper()
			wl := &kueue.Workload{}
			g.Expect(clnt.Get(managerTestCluster.ctx, key, wl)).To(gomega.Succeed())
			return wl
		}

		rayJob := testingrayjob.MakeJob("autoscaling-rayjob", f.managerNs.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(f.managerLq.Name).
			WithSubmissionMode(rayv1.InteractiveMode).
			EnableInTreeAutoscaling().
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayJob)

		var originSliceKey types.NamespacedName
		ginkgo.By("admitting the initial RayJob slice on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(managerTestCluster.client.List(managerTestCluster.ctx, workloads, client.InNamespace(rayJob.Namespace))).To(gomega.Succeed())
				g.Expect(workloads.Items).To(gomega.HaveLen(1))
				originSliceKey = client.ObjectKeyFromObject(&workloads.Items[0])
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, originSliceKey, admission(1))
		})

		remoteRayJob := &rayv1.RayJob{}
		ginkgo.By("creating the child RayCluster as KubeRay would", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayJob), remoteRayJob)).To(gomega.Succeed())
				g.Expect(remoteRayJob.Spec.Suspend).To(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			child := &rayv1.RayCluster{
				Name:            "autoscaling-rayjob-child",
				Namespace:       remoteRayJob.Namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(remoteRayJob, rayv1.GroupVersion.WithKind("RayJob"))},
				Spec:            *remoteRayJob.Spec.RayClusterSpec.DeepCopy(),
			}
			child.Spec.Suspend = new(false)
			jobframework.SetMultiKueueMeta(child, originSliceKey.Name, remoteRayJob.Labels[kueue.MultiKueueOriginLabel])
			behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, child)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayJob), remoteRayJob)).To(gomega.Succeed())
				remoteRayJob.Status.RayClusterName = child.Name
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, remoteRayJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				updatedRayJob := &rayv1.RayJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayJob), updatedRayJob)).To(gomega.Succeed())
				g.Expect(updatedRayJob.Annotations).To(gomega.HaveKeyWithValue(
					workloadraycluster.RayClusterPodsetReplicaSizesAnnotation,
					`[{"name":"workers-group-0","count":1}]`,
				))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("scaling the child RayCluster to create the first replacement slice", func() {
			child := &rayv1.RayCluster{}
			childKey := types.NamespacedName{Name: remoteRayJob.Status.RayClusterName, Namespace: remoteRayJob.Namespace}
			gomega.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, childKey, child)).To(gomega.Succeed())
			child.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](2)
			gomega.Expect(worker2TestCluster.client.Update(worker2TestCluster.ctx, child)).To(gomega.Succeed())
		})

		var activeSliceKey types.NamespacedName
		ginkgo.By("admitting the replacement created by the first child scale-up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(managerTestCluster.client.List(managerTestCluster.ctx, workloads, client.InNamespace(rayJob.Namespace))).To(gomega.Succeed())
				g.Expect(workloads.Items).To(gomega.HaveLen(2))
				for i := range workloads.Items {
					key := client.ObjectKeyFromObject(&workloads.Items[i])
					if key != originSliceKey {
						activeSliceKey = key
					}
				}
				g.Expect(activeSliceKey.Name).NotTo(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			originSlice := getWorkload(gomega.Default, managerTestCluster.client, originSliceKey)
			gomega.Eventually(func(g gomega.Gomega) {
				activeSlice := getWorkload(g, managerTestCluster.client, activeSliceKey)
				activeSlice.Status.ClusterName = originSlice.Status.ClusterName
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, activeSlice)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, activeSliceKey, admission(2).Obj())

			gomega.Eventually(func(g gomega.Gomega) {
				getWorkload(g, worker2TestCluster.client, activeSliceKey)
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, activeSliceKey, admission(2).Obj())
			behavioral.ExpectAdmissionCheckStateWithMessage(managerTestCluster.ctx, managerTestCluster.client, activeSliceKey,
				f.multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker2"`)

			gomega.Eventually(func(g gomega.Gomega) {
				originSlice := getWorkload(g, managerTestCluster.client, originSliceKey)
				finished := apimeta.FindStatusCondition(originSlice.Status.Conditions, kueue.WorkloadFinished)
				g.Expect(finished).NotTo(gomega.BeNil())
				g.Expect(finished.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(finished.Reason).To(gomega.Equal(kueue.WorkloadSliceReplaced))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayJob), remoteRayJob)).To(gomega.Succeed())
				g.Expect(jobframework.PrebuiltWorkloadNameFor(remoteRayJob)).To(gomega.Equal(activeSliceKey.Name))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating only the child RayCluster, which still references the finished origin slice", func() {
			child := &rayv1.RayCluster{}
			childKey := types.NamespacedName{Name: remoteRayJob.Status.RayClusterName, Namespace: remoteRayJob.Namespace}
			gomega.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, childKey, child)).To(gomega.Succeed())
			gomega.Expect(jobframework.PrebuiltWorkloadNameFor(child)).To(gomega.Equal(originSliceKey.Name))
			child.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](3)
			gomega.Expect(worker2TestCluster.client.Update(worker2TestCluster.ctx, child)).To(gomega.Succeed())
		})

		ginkgo.By("observing the child event reverse sync through the active slice", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				updatedRayJob := &rayv1.RayJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayJob), updatedRayJob)).To(gomega.Succeed())
				g.Expect(updatedRayJob.Annotations).To(gomega.HaveKeyWithValue(
					workloadraycluster.RayClusterPodsetReplicaSizesAnnotation,
					`[{"name":"workers-group-0","count":3}]`,
				))

				workloads := &kueue.WorkloadList{}
				g.Expect(managerTestCluster.client.List(managerTestCluster.ctx, workloads, client.InNamespace(rayJob.Namespace))).To(gomega.Succeed())
				g.Expect(workloads.Items).To(gomega.HaveLen(3))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should finish a replaced slice on the worker when the scheduler failed to", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueRayInTreeAutoscaling, true)

		admission := func(workerCount int32) *utiltestingapi.AdmissionWrapper {
			return utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
				utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
				utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(workerCount).Obj(),
				utiltestingapi.MakePodSetAssignment("submitter").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			)
		}

		rayJob := testingrayjob.MakeJob("autoscaling-rayjob-missed-finish", f.managerNs.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(f.managerLq.Name).
			WithSubmissionMode(rayv1.K8sJobMode).
			EnableInTreeAutoscaling().
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayJob)

		var originSliceKey types.NamespacedName
		ginkgo.By("admitting the initial RayJob slice on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(managerTestCluster.client.List(managerTestCluster.ctx, workloads, client.InNamespace(rayJob.Namespace))).To(gomega.Succeed())
				g.Expect(workloads.Items).To(gomega.HaveLen(1))
				originSliceKey = client.ObjectKeyFromObject(&workloads.Items[0])
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, originSliceKey, admission(1))
		})

		remoteRayJob := &rayv1.RayJob{}
		childKey := types.NamespacedName{Name: "autoscaling-rayjob-missed-finish-child", Namespace: rayJob.Namespace}
		ginkgo.By("creating the child RayCluster on worker2 as KubeRay would", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayJob), remoteRayJob)).To(gomega.Succeed())
				g.Expect(remoteRayJob.Spec.Suspend).To(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			child := &rayv1.RayCluster{
				Name:            childKey.Name,
				Namespace:       childKey.Namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(remoteRayJob, rayv1.GroupVersion.WithKind("RayJob"))},
				Spec:            *remoteRayJob.Spec.RayClusterSpec.DeepCopy(),
			}
			child.Spec.Suspend = new(false)
			jobframework.SetMultiKueueMeta(child, originSliceKey.Name, remoteRayJob.Labels[kueue.MultiKueueOriginLabel])
			behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, child)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayJob), remoteRayJob)).To(gomega.Succeed())
				remoteRayJob.Status.RayClusterName = child.Name
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, remoteRayJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("autoscaling the child RayCluster on worker2 to create a replacement slice", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				child := &rayv1.RayCluster{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, childKey, child)).To(gomega.Succeed())
				child.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](2)
				g.Expect(worker2TestCluster.client.Update(worker2TestCluster.ctx, child)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		var replacementSliceKey types.NamespacedName
		ginkgo.By("observing the replacement slice in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(managerTestCluster.client.List(managerTestCluster.ctx, workloads, client.InNamespace(rayJob.Namespace))).To(gomega.Succeed())
				g.Expect(workloads.Items).To(gomega.HaveLen(2))
				for i := range workloads.Items {
					if key := client.ObjectKeyFromObject(&workloads.Items[i]); key != originSliceKey {
						replacementSliceKey = key
					}
				}
				g.Expect(replacementSliceKey.Name).NotTo(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		// This suite does not run a scheduler, so the steps it would perform when admitting the
		// replacement are emulated.
		ginkgo.By("emulating the scheduler admitting the replacement slice on the manager cluster", func() {
			originSlice := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, originSliceKey, originSlice)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				replacement := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, replacementSliceKey, replacement)).To(gomega.Succeed())
				replacement.Status.ClusterName = originSlice.Status.ClusterName
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, replacement)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, replacementSliceKey, admission(2).Obj())
		})

		ginkgo.By("observing the replacement slice in the worker2 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, replacementSliceKey, &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		// The scheduler admits the replacement, but its attempt to finish the origin slice fails,
		// so the origin slice is left unfinished on the worker.
		ginkgo.By("emulating the scheduler admitting the replacement slice on worker2 without finishing the origin slice", func() {
			behavioral.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, replacementSliceKey, admission(2).Obj())
		})

		ginkgo.By("observing the job reconciler finish the origin slice on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				originSlice := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, originSliceKey, originSlice)).To(gomega.Succeed())
				finished := apimeta.FindStatusCondition(originSlice.Status.Conditions, kueue.WorkloadFinished)
				g.Expect(finished).NotTo(gomega.BeNil())
				g.Expect(finished.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(finished.Reason).To(gomega.Equal(kueue.WorkloadSliceReplaced))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observing the replacement slice and the remote objects are kept on worker2", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				replacement := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, replacementSliceKey, replacement)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(replacement.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeFalse())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayJob), &rayv1.RayJob{})).To(gomega.Succeed())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, childKey, &rayv1.RayCluster{})).To(gomega.Succeed())
			}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a RayCluster on worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		raycluster := testingraycluster.MakeCluster("raycluster1", f.managerNs.Name).
			Queue(f.managerLq.Name).
			WithHistoryServerOptions(&rayv1.HistoryServerOptions{
				CollectorOptions: &rayv1.CollectorOptions{
					Image: new("quay.io/kuberay/collector:v1.7.0"),
				},
			}).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, raycluster)
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: f.managerNs.Name}
		behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("propagating history server options to the worker RayCluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayCluster := rayv1.RayCluster{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(raycluster), &createdRayCluster)).To(gomega.Succeed())
				g.Expect(createdRayCluster.Spec.HistoryServerOptions).To(gomega.Equal(raycluster.Spec.HistoryServerOptions))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("changing the status of the RayCluster in the worker, updates the manager's RayCluster status", func() {
			createdRayCluster := rayv1.RayCluster{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(raycluster), &createdRayCluster)).To(gomega.Succeed())
				createdRayCluster.Status.DesiredWorkerReplicas = 1
				createdRayCluster.Status.ReadyWorkerReplicas = 1
				createdRayCluster.Status.AvailableWorkerReplicas = 1
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayCluster)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(raycluster), &createdRayCluster)).To(gomega.Succeed())
				g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.ReadyWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.AvailableWorkerReplicas).To(gomega.Equal(int32(1)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should forward a serveConfigV2 update to the RayService on the worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		rayService := testingrayservice.MakeService("rayservice1", f.managerNs.Name).
			Queue(f.managerLq.Name).
			UpgradeStrategy(rayv1.RayServiceUpgradeNone).
			WithServeConfigV2("serve-config-v1").
			WithHistoryServerOptions(&rayv1.HistoryServerOptions{
				CollectorOptions: &rayv1.CollectorOptions{
					Image: new("quay.io/kuberay/collector:v1.7.0"),
				},
			}).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayService)
		wlLookupKey := types.NamespacedName{Name: workloadrayservice.GetWorkloadNameForRayService(rayService.Name, rayService.UID), Namespace: f.managerNs.Name}
		behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("checking the remote RayService is created with the initial serveConfigV2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := rayv1.RayService{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayService), &createdRayService)).To(gomega.Succeed())
				g.Expect(createdRayService.Spec.ServeConfigV2).To(gomega.Equal("serve-config-v1"))
				g.Expect(createdRayService.Spec.RayClusterSpec.HistoryServerOptions).To(gomega.Equal(rayService.Spec.RayClusterSpec.HistoryServerOptions))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating serveConfigV2 on the manager, the change is forwarded to the remote RayService", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := rayv1.RayService{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayService), &createdRayService)).To(gomega.Succeed())
				createdRayService.Spec.ServeConfigV2 = "serve-config-v2"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, &createdRayService)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := rayv1.RayService{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayService), &createdRayService)).To(gomega.Succeed())
				g.Expect(createdRayService.Spec.ServeConfigV2).To(gomega.Equal("serve-config-v2"))
				g.Expect(createdRayService.Spec.RayClusterSpec.HistoryServerOptions).To(gomega.Equal(rayService.Spec.RayClusterSpec.HistoryServerOptions))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
