/*
Copyright 2023 The Kubernetes Authors.

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
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Multikueue", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultikueueSecret *corev1.Secret
		managerMultiKueueConfig *kueue.MultiKueueConfig
		managerAc               *kueue.AdmissionCheck
		managerCq               *kueue.ClusterQueue
		managerLq               *kueue.LocalQueue

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
	)
	ginkgo.BeforeEach(func() {
		managerNs = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "multikueue-",
			},
		}
		gomega.Expect(managerCluster.client.Create(managerCluster.ctx, managerNs)).To(gomega.Succeed())

		worker1Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(worker1Cluster.client.Create(worker1Cluster.ctx, worker1Ns)).To(gomega.Succeed())

		worker2Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(worker2Cluster.client.Create(worker2Cluster.ctx, worker2Ns)).To(gomega.Succeed())

		w1Kubeconfig, err := worker1Cluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2Cluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultikueueSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue",
				Namespace: managerNs.Name,
			},
			Data: map[string][]byte{
				"worker1.kubeconfig": w1Kubeconfig,
				"worker2.kubeconfig": w2Kubeconfig,
			},
		}

		gomega.Expect(managerCluster.client.Create(managerCluster.ctx, managerMultikueueSecret)).To(gomega.Succeed())

		managerMultiKueueConfig = &kueue.MultiKueueConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: "multikueueconfig",
			},
			Spec: kueue.MultiKueueConfigSpec{
				Clusters: []kueue.MultiKueueCluster{
					{
						Name: "worker1",
						KubeconfigRef: kueue.KubeconfigRef{
							SecretName:      "multikueue",
							SecretNamespace: managerNs.Name,
							ConfigKey:       "worker1.kubeconfig",
						},
					},
					{
						Name: "worker2",
						KubeconfigRef: kueue.KubeconfigRef{
							SecretName:      "multikueue",
							SecretNamespace: managerNs.Name,
							ConfigKey:       "worker2.kubeconfig",
						},
					},
				},
			},
		}
		gomega.Expect(managerCluster.client.Create(managerCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		managerAc = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(multikueue.ControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerCluster.client.Create(managerCluster.ctx, managerAc)).Should(gomega.Succeed())

		ginkgo.By("wait for check active", func() {
			updatetedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(managerAc)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerCluster.client.Get(managerCluster.ctx, acKey, &updatetedAc)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(updatetedAc.Status.Conditions, kueue.AdmissionCheckActive)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue), "Reason: %s, Message: %q", cond.Reason, cond.Status)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

		})

		managerCq = utiltesting.MakeClusterQueue("q1").
			AdmissionChecks(managerAc.Name).
			Obj()
		gomega.Expect(managerCluster.client.Create(managerCluster.ctx, managerCq)).Should(gomega.Succeed())

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		gomega.Expect(managerCluster.client.Create(managerCluster.ctx, managerLq)).Should(gomega.Succeed())

		worker1Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker1Cluster.client.Create(worker1Cluster.ctx, worker1Cq)).Should(gomega.Succeed())
		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		gomega.Expect(worker1Cluster.client.Create(worker1Cluster.ctx, worker1Lq)).Should(gomega.Succeed())

		worker2Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker2Cluster.client.Create(worker2Cluster.ctx, worker2Cq)).Should(gomega.Succeed())
		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		gomega.Expect(worker2Cluster.client.Create(worker2Cluster.ctx, worker2Lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerCluster.ctx, managerCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1Cluster.ctx, worker1Cluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2Cluster.ctx, worker2Cluster.client, worker2Ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(managerCluster.ctx, managerCluster.client, managerCq, true)
		util.ExpectClusterQueueToBeDeleted(worker1Cluster.ctx, worker1Cluster.client, worker1Cq, true)
		util.ExpectClusterQueueToBeDeleted(worker2Cluster.ctx, worker2Cluster.client, worker2Cq, true)
		util.ExpectAdmissionCheckToBeDeleted(managerCluster.ctx, managerCluster.client, managerAc, true)
		gomega.Expect(managerCluster.client.Delete(managerCluster.ctx, managerMultiKueueConfig)).To(gomega.Succeed())
	})

	ginkgo.It("Should run a job on worker if admitted", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(managerLq.Name).
			Obj()
		gomega.Expect(managerCluster.client.Create(managerCluster.ctx, job)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the master", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerCluster.client.Get(managerCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerCluster.ctx, managerCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerCluster.client.Get(managerCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1Cluster.client.Get(worker1Cluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2Cluster.client.Get(worker2Cluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, acs is update on master amd worker2 wl is removed", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1Cluster.client.Get(worker1Cluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1Cluster.ctx, worker1Cluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerCluster.client.Get(managerCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, managerAc.Name)
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStatePending))
				g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker1"`))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2Cluster.client.Get(worker2Cluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1Cluster.client.Get(worker1Cluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions, batchv1.JobCondition{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      metav1.Now(),
					LastTransitionTime: metav1.Now(),
				})
				g.Expect(worker1Cluster.client.Status().Update(worker1Cluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerCluster.client.Get(managerCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "JobFinished",
					Message: `From remote "worker1": Job finished successfully`,
				}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1Cluster.client.Get(worker1Cluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		})
	})

})
