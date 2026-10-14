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

package baseline

import (
	"bytes"
	"encoding/json"
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// Sanity check that the compiled kueuectl binary works end-to-end.
// API persistence cases live in the integration suite.
var _ = ginkgo.Describe("Kueuectl", ginkgo.Label("area:singlecluster", "feature:kueuectl"), func() {
	var (
		ns *corev1.Namespace
		cq *kueue.ClusterQueue
		rf *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		rf = nil
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")

		cq = utiltestingapi.MakeClusterQueue("e2e-cq-" + ns.Name).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		if rf != nil {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		}
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should create a local queue and list it back", func() {
		lqName := "e2e-lq-sanity"

		ginkgo.By("Create local queue via the kueuectl binary", func() {
			cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName,
				"--clusterqueue", cq.Name, "--namespace", ns.Name)
			output, err := cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
		})

		ginkgo.By("List local queues via the kueuectl binary and verify the new one appears", func() {
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(kueuectlPath, "list", "localqueue",
				"--namespace", ns.Name, "-o", "json")
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", stderr.String())

			var list kueue.LocalQueueList
			gomega.Expect(json.Unmarshal(stdout.Bytes(), &list)).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.ContainElement(gomega.SatisfyAll(
				gomega.HaveField("Name", lqName),
				gomega.HaveField("Spec.ClusterQueue", kueue.ClusterQueueReference(cq.Name)),
			)))
		})
	})

	ginkgo.It("Should schedule a Job with an explicit empty toleration from a non-TAS resource flavor", func() {
		rfName := "empty-toleration-" + ns.Name
		rf = utiltestingapi.MakeResourceFlavor(rfName).Obj()

		ginkgo.By("Create a non-TAS resource flavor through the CLI", func() {
			cmd := exec.Command(kueuectlPath, "create", "resourceflavor", rfName,
				"--node-labels", corev1.LabelOSStable+"=linux",
				"--tolerations", "example.com/empty=:NoSchedule")
			output, err := cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: rfName}, rf)).To(gomega.Succeed())
			gomega.Expect(rf.Spec.TopologyName).To(gomega.BeNil())
		})

		ginkgo.By("Provide quota for the flavor and a local queue", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			original := cq.DeepCopy()
			cq.Spec = utiltestingapi.MakeClusterQueue(cq.Name).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rfName).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "1Gi").Obj()).Obj().Spec
			gomega.Expect(k8sClient.Patch(ctx, cq, client.MergeFrom(original))).To(gomega.Succeed())
			lq := utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		job := testingjob.MakeJob("empty-toleration", ns.Name).
			Queue("main").
			RequestAndLimit(corev1.ResourceCPU, "100m").
			RequestAndLimit(corev1.ResourceMemory, "20Mi").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job)
		e2e.ExpectJobToBeCompleted(ctx, k8sClient, job)

		ginkgo.By("Check that the Job and its scheduled pod preserve Equal rather than Exists", func() {
			wantToleration := corev1.Toleration{
				Key: "example.com/empty", Operator: corev1.TolerationOpEqual,
				Effect: corev1.TaintEffectNoSchedule,
			}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			gomega.Expect(job.Spec.Template.Spec.NodeSelector).To(gomega.HaveKeyWithValue(corev1.LabelOSStable, "linux"))
			gomega.Expect(job.Spec.Template.Spec.Tolerations).To(gomega.ContainElement(wantToleration))
			pods := &corev1.PodList{}
			gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels{"batch.kubernetes.io/job-name": job.Name})).To(gomega.Succeed())
			gomega.Expect(pods.Items).To(gomega.ContainElement(gomega.SatisfyAll(
				gomega.HaveField("Spec.NodeName", gomega.Not(gomega.BeEmpty())),
				gomega.HaveField("Status.Phase", gomega.Equal(corev1.PodSucceeded)),
				gomega.HaveField("Spec.Tolerations", gomega.ContainElement(wantToleration)),
			)))
		})
	})
})
