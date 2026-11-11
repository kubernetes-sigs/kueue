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

package job

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Job with pinned self anti-affinity", func() {
	var (
		ns       *corev1.Namespace
		node     *corev1.Node
		topology *kueue.Topology
		flavor   *kueue.ResourceFlavor
		cq       *kueue.ClusterQueue
		lq       *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		configuration := &config.Configuration{WaitForPodsReady: &config.WaitForPodsReady{
			Timeout: metav1.Duration{Duration: 2 * time.Second},
		}}
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(true, true, configuration))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "job-anti-affinity-")
		node = testingnode.MakeNode("anti-affinity-node").
			Label("node-group", "job-anti-affinity").
			Label(corev1.LabelHostname, "anti-affinity-node").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).Ready().Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})
		topology = utiltestingapi.MakeTopology("job-anti-affinity").Levels(corev1.LabelHostname).Obj()
		behavioral.MustCreate(ctx, k8sClient, topology)
		flavor = utiltestingapi.MakeResourceFlavor("job-anti-affinity").
			NodeLabel("node-group", "job-anti-affinity").TopologyName(topology.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("job-anti-affinity").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "1").Obj()).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("deactivates after timeout and does not re-admit", framework.SlowSpec, func() {
		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodAnnotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
			Parallelism(2).Completions(2).
			NodeSelector(corev1.LabelHostname, node.Name).
			PodLabel("app", "anti-affinity-test").
			PodAffinity(&corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
					TopologyKey:   corev1.LabelHostname,
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "anti-affinity-test"}},
				}},
			}}).
			Request(corev1.ResourceCPU, "100m").Obj()
		behavioral.MustCreate(ctx, k8sClient, job)
		key := client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID)}
		wl := &kueue.Workload{}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(workload.IsActive(wl)).To(gomega.BeFalse())
			g.Expect(apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.HaveField("Reason",
				"DeactivatedDueTo"+kueue.WorkloadPodAntiAffinityTopologyConflict))
			g.Expect(wl.Status.RequeueState).To(gomega.BeNil())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			g.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
			g.Expect(workload.IsActive(wl)).To(gomega.BeFalse())
			g.Expect(wl.Status.RequeueState).To(gomega.BeNil())
		}, 3*time.Second, behavioral.ShortInterval).Should(gomega.Succeed())
	})
})
