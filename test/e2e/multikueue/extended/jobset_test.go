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

package extended

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/util"
)

type jobSetTestContext struct {
	managerNs         *corev1.Namespace
	managerLq         *kueue.LocalQueue
	multiKueueAc      *kueue.AdmissionCheck
	kubernetesClients kubernetesClientsMap
}

func registerJobSetTests(contextProvider func() jobSetTestContext) {
	ginkgo.It("Should run a jobSet on worker if admitted", ginkgo.Label("feature:jobset"), func() {
		tc := contextProvider()
		managerNs := tc.managerNs
		managerLq := tc.managerLq
		multiKueueAc := tc.multiKueueAc
		kubernetesClients := tc.kubernetesClients

		jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
			Queue(managerLq.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    2,
					Parallelism: 2,
					Completions: 2,
					Image:       util.GetAgnHostImage(),
					// Give it the time to be observed Active in the live status update step.
					Args: util.BehaviorWaitForDeletion,
				},
			).
			RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "100m").
			RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "100M").
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the jobSet", func() {
			util.MustCreate(ctx, k8sManagerClient, jobSet)
		})

		createdLeaderWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

		admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
		admittedWorker := kubernetesClients[admittedWorkerName]

		ginkgo.By("Waiting for the jobSet to get status updates", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobset := &jobset.JobSet{}
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobset)).To(gomega.Succeed())

				g.Expect(createdJobset.Status.ReplicatedJobsStatus).To(gomega.BeComparableTo([]jobset.ReplicatedJobStatus{
					{
						Name:   "replicated-job-1",
						Ready:  2,
						Active: 2,
					},
				}, cmpopts.IgnoreFields(jobset.ReplicatedJobStatus{}, "Succeeded", "Failed")))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Finishing the jobset pods", func() {
			listOpts := util.GetListOptsFromLabel(fmt.Sprintf("jobset.sigs.k8s.io/jobset-name=%s", jobSet.Name))
			util.WaitForActivePodsAndTerminate(ctx, admittedWorker.client, admittedWorker.restClient, admittedWorker.cfg, jobSet.Namespace, 4, 0, listOpts)
		})

		ginkgo.By("Waiting for the jobSet to finish", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())

				g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadFinishedReasonSucceeded,
					Message: "jobset completed successfully",
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking no objects are left in the worker clusters and the jobSet is completed", func() {
			util.ExpectObjectToBeDeletedOnClusters(ctx, createdLeaderWorkload, k8sWorker1Client, k8sWorker2Client)
			util.ExpectObjectToBeDeletedOnClusters(ctx, jobSet, k8sWorker1Client, k8sWorker2Client)

			createdJobSet := &jobset.JobSet{}
			gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
			gomega.Expect(ptr.Deref(createdJobSet.Spec.Suspend, true)).To(gomega.BeFalse())
			gomega.Expect(createdJobSet.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
				metav1.Condition{
					Type:    string(jobset.JobSetCompleted),
					Status:  metav1.ConditionTrue,
					Reason:  "AllJobsCompleted",
					Message: "jobset completed successfully",
				},
				util.IgnoreConditionTimestampsAndObservedGeneration)))
		})
	})
}
