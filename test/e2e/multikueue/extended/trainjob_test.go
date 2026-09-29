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
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/test/util"
)

type trainJobTestContext struct {
	managerNs    *corev1.Namespace
	managerLq    *kueue.LocalQueue
	multiKueueAc *kueue.AdmissionCheck
}

func registerTrainJobTests(contextProvider func() trainJobTestContext) {
	ginkgo.It("Should run a TrainJob on worker if admitted", ginkgo.Label("feature:trainjob"), func() {
		tc := contextProvider()
		managerNs := tc.managerNs
		managerLq := tc.managerLq
		multiKueueAc := tc.multiKueueAc

		trainjob := testingtrainjob.MakeTrainJob("trainjob-test", managerNs.Name).
			RuntimeRefName("torch-distributed").
			Queue(managerLq.Name).
			RequestAndLimit(corev1.ResourceCPU, "100m", "100m").
			RequestAndLimit(corev1.ResourceMemory, "100M", "100M").
			// Even if we override the image coming from the TrainingRuntime, we still need to set the command and args
			TrainerImage(util.GetAgnHostImage(), []string{"/agnhost"}, util.BehaviorExitFast).
			Obj()

		ginkgo.By("Creating the trainjob", func() {
			util.MustCreate(ctx, k8sManagerClient, trainjob)
		})

		wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainjob.Name, trainjob.UID), Namespace: managerNs.Name}

		admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
		ginkgo.GinkgoLogr.Info("TrainJob %s is admitted in worker cluster %s", trainjob.Name, admittedWorker)

		ginkgo.By("Checking the TrainJob is ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdTrainJob := &kftrainer.TrainJob{}
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(trainjob), createdTrainJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).To(gomega.BeFalse())
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
}
