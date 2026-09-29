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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadaw "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

type appWrapperTestContext struct {
	managerNs         *corev1.Namespace
	managerLq         *kueue.LocalQueue
	multiKueueAc      *kueue.AdmissionCheck
	kubernetesClients kubernetesClientsMap
}

func registerAppWrapperTests(contextProvider func() appWrapperTestContext) {
	ginkgo.It("Should run an appwrapper containing a job on worker if admitted", ginkgo.Label("feature:appwrapper"), func() {
		tc := contextProvider()
		managerNs := tc.managerNs
		managerLq := tc.managerLq
		multiKueueAc := tc.multiKueueAc
		kubernetesClients := tc.kubernetesClients

		jobName := "job-1"
		aw := testingaw.MakeAppWrapper("aw", managerNs.Name).
			Queue(managerLq.Name).
			Component(testingaw.Component{
				Template: testingjob.MakeJob(jobName, managerNs.Name).
					SetTypeMeta().
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion). // Give it the time to be observed Active in the live status update step.
					Parallelism(2).
					RequestAndLimit(corev1.ResourceCPU, "100m").
					RequestAndLimit(corev1.ResourceMemory, "100M").
					TerminationGracePeriod(1).
					SetTypeMeta().Obj(),
			}).
			Obj()

		ginkgo.By("Creating the appwrapper", func() {
			util.MustCreate(ctx, k8sManagerClient, aw)
		})

		wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: managerNs.Name}

		admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
		admittedWorker := kubernetesClients[admittedWorkerName]

		ginkgo.By("Waiting for the appwrapper to get status updates", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperRunning))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Finishing the wrapped job's pods", func() {
			listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobName))
			util.WaitForActivePodsAndTerminate(ctx, admittedWorker.client, admittedWorker.restClient, admittedWorker.cfg, aw.Namespace, 2, 0, listOpts)
		})

		ginkgo.By("Waiting for the appwrapper to finish", func() {
			util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
		})

		ginkgo.By("Checking no objects are left in the worker clusters and the appwrapper is completed", func() {
			createdWorkload := &kueue.Workload{}
			gomega.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			util.ExpectObjectToBeDeletedOnClusters(ctx, createdWorkload, k8sWorker1Client, k8sWorker2Client)
			util.ExpectObjectToBeDeletedOnClusters(ctx, aw, k8sWorker1Client, k8sWorker2Client)

			createdAppWrapper := &awv1beta2.AppWrapper{}
			gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
			gomega.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
			gomega.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperSucceeded))
		})
	})
}
