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
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

type mpiJobTestContext struct {
	managerNs    *corev1.Namespace
	managerLq    *kueue.LocalQueue
	multiKueueAc *kueue.AdmissionCheck
}

func registerMPIJobTests(contextProvider func() mpiJobTestContext) {
	ginkgo.It("Should run a MPIJob on worker if admitted", ginkgo.Label("feature:mpijob"), func() {
		tc := contextProvider()
		managerNs := tc.managerNs
		managerLq := tc.managerLq
		multiKueueAc := tc.multiKueueAc

		mpijob := testingmpijob.MakeMPIJob("mpijob1", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			MPIJobReplicaSpecs(
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
					ReplicaCount:  1,
					RestartPolicy: "OnFailure",
					Image:         behavioral.GetAgnHostImage(),
					Args:          behavioral.BehaviorExitFast,
				},
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeWorker,
					ReplicaCount:  1,
					RestartPolicy: "OnFailure",
					Image:         behavioral.GetAgnHostImage(),
					Args:          behavioral.BehaviorExitFast,
				},
			).
			RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "100m").
			RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceMemory, "100M").
			RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "100m").
			RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceMemory, "100M").
			Obj()

		ginkgo.By("Creating the MPIJob", func() {
			behavioral.MustCreate(ctx, k8sManagerClient, mpijob)
		})

		wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: managerNs.Name}

		admittedWorker := behavioral.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
		ginkgo.GinkgoLogr.Info("MPIJob %s is admitted in worker cluster %s", mpijob.Name, admittedWorker)

		ginkgo.By("Waiting for the MPIJob to finish", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdMPIJob := &kfmpi.MPIJob{}
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(mpijob), createdMPIJob)).To(gomega.Succeed())
				g.Expect(createdMPIJob.Status.ReplicaStatuses[kfmpi.MPIReplicaTypeLauncher]).To(gomega.BeComparableTo(
					&kfmpi.ReplicaStatus{
						Active:    0,
						Succeeded: 1,
					},
				))
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
		})

		ginkgo.By("Checking no objects are left in the worker clusters and the MPIJob is completed", func() {
			wl := &kueue.Workload{
				Name:      wlLookupKey.Name,
				Namespace: wlLookupKey.Namespace,
			}
			behavioral.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
			behavioral.ExpectObjectToBeDeletedOnClusters(ctx, mpijob, k8sWorker1Client, k8sWorker2Client)
		})
	})
}
