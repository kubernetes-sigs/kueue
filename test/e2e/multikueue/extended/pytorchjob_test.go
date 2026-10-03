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

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

type pyTorchJobTestContext struct {
	managerNs    *corev1.Namespace
	managerLq    *kueue.LocalQueue
	multiKueueAc *kueue.AdmissionCheck
}

func registerPyTorchJobTests(contextProvider func() pyTorchJobTestContext) {
	ginkgo.It("Should run a kubeflow PyTorchJob on worker if admitted", ginkgo.Label("feature:pytorchjob"), func() {
		tc := contextProvider()
		managerNs := tc.managerNs
		managerLq := tc.managerLq
		multiKueueAc := tc.multiKueueAc

		pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob1", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(managerLq.Name).
			PyTorchReplicaSpecs(
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
					ReplicaCount:  1,
					RestartPolicy: "Never",
					Image:         behavioral.GetAgnHostImage(),
					Args:          behavioral.BehaviorExitFast,
				},
			).
			RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "100m").
			RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceMemory, "100M").
			RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "100m").
			RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceMemory, "100M").
			Obj()

		ginkgo.By("Creating the PyTorchJob", func() {
			behavioral.MustCreate(ctx, k8sManagerClient, pyTorchJob)
		})

		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}

		admittedWorker := behavioral.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
		ginkgo.GinkgoLogr.Info("PyTorchJob %s is admitted in worker cluster %s", pyTorchJob.Name, admittedWorker)

		ginkgo.By("Waiting for the PyTorchJob to finish", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPyTorchJob := &kftraining.PyTorchJob{}
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pyTorchJob), createdPyTorchJob)).To(gomega.Succeed())
				g.Expect(createdPyTorchJob.Status.ReplicaStatuses[kftraining.PyTorchJobReplicaTypeMaster]).To(gomega.BeComparableTo(
					&kftraining.ReplicaStatus{
						Active:    0,
						Succeeded: 1,
						Selector: fmt.Sprintf(
							"training.kubeflow.org/job-name=%s,training.kubeflow.org/operator-name=pytorchjob-controller,training.kubeflow.org/replica-type=master",
							createdPyTorchJob.Name,
						),
					},
				))
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
		})

		ginkgo.By("Checking no objects are left in the worker clusters and the PyTorchJob is completed", func() {
			wl := &kueue.Workload{
				Name:      wlLookupKey.Name,
				Namespace: wlLookupKey.Namespace,
			}
			behavioral.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
			behavioral.ExpectObjectToBeDeletedOnClusters(ctx, pyTorchJob, k8sWorker1Client, k8sWorker2Client)
		})
	})
}
