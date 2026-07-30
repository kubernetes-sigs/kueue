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

package centralizedtas

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/centralizedtas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	tasNodeGroupLabel = "cloud.provider.com/node-group"
	instanceType      = "tas-node"

	// passThroughCPU is effectively infinite worker quota; workers never decide admission.
	passThroughCPU    = "10000"
	passThroughMemory = "10000Gi"
)

type workerClusterClients struct {
	client     client.Client
	restClient *rest.RESTClient
	cfg        *rest.Config
}

type centralizedTASFixture struct {
	managerNs *corev1.Namespace
	worker1Ns *corev1.Namespace
	worker2Ns *corev1.Namespace

	workerCluster1   *kueue.MultiKueueCluster
	workerCluster2   *kueue.MultiKueueCluster
	multiKueueConfig *kueue.MultiKueueConfig
	multiKueueAc     *kueue.AdmissionCheck

	managerTopology *kueue.Topology
	managerFlavor   *kueue.ResourceFlavor

	worker1Topology *kueue.Topology
	worker1Flavor   *kueue.ResourceFlavor
	worker2Topology *kueue.Topology
	worker2Flavor   *kueue.ResourceFlavor

	managerCQs []*kueue.ClusterQueue
	workers    map[string]workerClusterClients
}

type managerCQSpec struct {
	name              string
	generated         bool
	cpu               string
	memory            string
	cohort            kueue.CohortReference
	enablePreemption  bool
}

func setupCentralizedTASFixture(cqs ...managerCQSpec) *centralizedTASFixture {
	ginkgo.GinkgoHelper()
	if len(cqs) == 0 {
		cqs = []managerCQSpec{{generated: true, cpu: "8", memory: "8Gi"}}
	}

	f := &centralizedTASFixture{
		managerNs: util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "ctas-"),
		workers: map[string]workerClusterClients{
			"": {client: k8sManagerClient, restClient: managerRestClient, cfg: managerCfg},
		},
	}
	f.worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, f.managerNs.Name)
	f.worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, f.managerNs.Name)

	f.workerCluster1 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker1-").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
	util.MustCreate(ctx, k8sManagerClient, f.workerCluster1)
	f.workerCluster2 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker2-").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
	util.MustCreate(ctx, k8sManagerClient, f.workerCluster2)

	f.multiKueueConfig = utiltestingapi.MakeMultiKueueConfigWithGeneratedName("mkcfg-").
		Clusters(f.workerCluster1.Name, f.workerCluster2.Name).Obj()
	util.MustCreate(ctx, k8sManagerClient, f.multiKueueConfig)

	waitForMultiKueueClustersActive(f.workerCluster1, f.workerCluster2)

	f.multiKueueAc = utiltestingapi.MakeAdmissionCheck("").
		GeneratedName("mkac-").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", f.multiKueueConfig.Name).
		Obj()
	util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, f.multiKueueAc)

	f.managerTopology = utiltestingapi.MakeTopology("cluster-host-" + f.managerNs.Name).
		Levels(centralizedtas.ClusterLabel, corev1.LabelHostname).
		Obj()
	util.MustCreate(ctx, k8sManagerClient, f.managerTopology)

	f.managerFlavor = utiltestingapi.MakeResourceFlavor("").
		GeneratedName("tas-flavor-").
		NodeLabel(tasNodeGroupLabel, instanceType).
		TopologyName(f.managerTopology.Name).
		Obj()
	util.MustCreate(ctx, k8sManagerClient, f.managerFlavor)

	f.worker1Topology = utiltestingapi.MakeDefaultOneLevelTopology("host-" + f.worker1Ns.Name)
	util.MustCreate(ctx, k8sWorker1Client, f.worker1Topology)
	f.worker1Flavor = utiltestingapi.MakeResourceFlavor(f.managerFlavor.Name).
		NodeLabel(tasNodeGroupLabel, instanceType).
		TopologyName(f.worker1Topology.Name).
		Obj()
	util.MustCreate(ctx, k8sWorker1Client, f.worker1Flavor)

	f.worker2Topology = utiltestingapi.MakeDefaultOneLevelTopology("host-" + f.worker2Ns.Name)
	util.MustCreate(ctx, k8sWorker2Client, f.worker2Topology)
	f.worker2Flavor = utiltestingapi.MakeResourceFlavor(f.managerFlavor.Name).
		NodeLabel(tasNodeGroupLabel, instanceType).
		TopologyName(f.worker2Topology.Name).
		Obj()
	util.MustCreate(ctx, k8sWorker2Client, f.worker2Flavor)

	f.workers[f.workerCluster1.Name] = workerClusterClients{
		client: k8sWorker1Client, restClient: worker1RestClient, cfg: worker1Cfg,
	}
	f.workers[f.workerCluster2.Name] = workerClusterClients{
		client: k8sWorker2Client, restClient: worker2RestClient, cfg: worker2Cfg,
	}

	for _, spec := range cqs {
		var cq *kueue.ClusterQueue
		if spec.generated {
			cq = utiltestingapi.MakeClusterQueue("").
				GeneratedName("cq-").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(f.managerFlavor.Name).
					Resource(corev1.ResourceCPU, spec.cpu).
					Resource(corev1.ResourceMemory, spec.memory).
					Obj()).
				AdmissionChecks(kueue.AdmissionCheckReference(f.multiKueueAc.Name)).
				Obj()
		} else {
			cq = utiltestingapi.MakeClusterQueue(spec.name).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(f.managerFlavor.Name).
					Resource(corev1.ResourceCPU, spec.cpu).
					Resource(corev1.ResourceMemory, spec.memory).
					Obj()).
				AdmissionChecks(kueue.AdmissionCheckReference(f.multiKueueAc.Name)).
				Obj()
		}
		if spec.cohort != "" {
			cq.Spec.CohortName = spec.cohort
		}
		if spec.enablePreemption {
			cq.Spec.Preemption = &kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}
		}
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, cq)
		f.managerCQs = append(f.managerCQs, cq)
		createPassThroughWorkerCQ(k8sWorker1Client, cq.Name, f.worker1Flavor.Name)
		createPassThroughWorkerCQ(k8sWorker2Client, cq.Name, f.worker2Flavor.Name)
	}

	return f
}

func createPassThroughWorkerCQ(k8sClient client.Client, cqName string, flavorName string) {
	ginkgo.GinkgoHelper()
	cq := utiltestingapi.MakeClusterQueue(cqName).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorName).
			Resource(corev1.ResourceCPU, passThroughCPU).
			Resource(corev1.ResourceMemory, passThroughMemory).
			Obj()).
		Obj()
	util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
}

func createManagerLQ(f *centralizedTASFixture, cqName, lqName string) *kueue.LocalQueue {
	ginkgo.GinkgoHelper()
	lq := utiltestingapi.MakeLocalQueue(lqName, f.managerNs.Name).ClusterQueue(cqName).Obj()
	util.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, lq)
	createPassThroughLQ(f, cqName, lqName)
	return lq
}

func createPassThroughLQ(f *centralizedTASFixture, cqName, lqName string) {
	ginkgo.GinkgoHelper()
	for _, ns := range []*corev1.Namespace{f.worker1Ns, f.worker2Ns} {
		var k8sClient client.Client
		if ns == f.worker1Ns {
			k8sClient = k8sWorker1Client
		} else {
			k8sClient = k8sWorker2Client
		}
		lq := utiltestingapi.MakeLocalQueue(lqName, ns.Name).ClusterQueue(cqName).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	}
}

func waitForMultiKueueClustersActive(clusters ...*kueue.MultiKueueCluster) {
	ginkgo.GinkgoHelper()
	for _, cluster := range clusters {
		gomega.Eventually(func(g gomega.Gomega) {
			updated := &kueue.MultiKueueCluster{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(cluster), updated)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updated.Status.Conditions, kueue.MultiKueueClusterActive)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	}
}

func getTASWorkerNode(k8sClient client.Client) *corev1.Node {
	ginkgo.GinkgoHelper()
	nodes := &corev1.NodeList{}
	gomega.Expect(k8sClient.List(ctx, nodes, client.MatchingLabels{tasNodeGroupLabel: instanceType})).To(gomega.Succeed())
	gomega.Expect(nodes.Items).NotTo(gomega.BeEmpty())
	return &nodes.Items[0]
}

func createNonTASPodOnNode(k8sClient client.Client, ns, name, nodeName, cpu string) *corev1.Pod {
	ginkgo.GinkgoHelper()
	pod := testingpod.MakePod(name, ns).
		Request(corev1.ResourceCPU, cpu).
		Request(corev1.ResourceMemory, "128Mi").
		NodeName(nodeName).
		Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
		TerminationGracePeriod(0).
		Obj()
	util.MustCreate(ctx, k8sClient, pod)
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
		g.Expect(pod.Spec.NodeName).To(gomega.Equal(nodeName))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	return pod
}

func cpuRequestToSaturateNode(node *corev1.Node, reserve resource.Quantity) string {
	alloc := node.Status.Allocatable[corev1.ResourceCPU]
	alloc.Sub(reserve)
	if alloc.Sign() <= 0 {
		return "1"
	}
	return alloc.String()
}

func cpuPerPodToSaturateNode(node *corev1.Node, podCount int32, reserve resource.Quantity) string {
	alloc := node.Status.Allocatable[corev1.ResourceCPU]
	alloc.Sub(reserve)
	if alloc.Sign() <= 0 {
		return "1"
	}
	perPod := alloc.Value() / int64(podCount)
	if perPod < 1 {
		perPod = 1
	}
	return fmt.Sprintf("%d", perPod)
}

func createTASJob(name, ns, lqName string, parallelism int32, cpu string) *batchv1.Job {
	return createTASJobWithWPC(name, ns, lqName, "", parallelism, cpu)
}

func createTASJobWithWPC(name, ns, lqName, wpc string, parallelism int32, cpu string) *batchv1.Job {
	ginkgo.GinkgoHelper()
	job := testingjob.MakeJob(name, ns).
		Queue(kueue.LocalQueueName(lqName)).
		Parallelism(parallelism).
		Completions(parallelism).
		RequestAndLimit(corev1.ResourceCPU, cpu).
		RequestAndLimit(corev1.ResourceMemory, "128Mi").
		TerminationGracePeriod(1).
		Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
		Obj()
	w := &testingjob.JobWrapper{Job: *job}
	if wpc != "" {
		w = w.WorkloadPriorityClass(wpc)
	}
	return w.PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).Obj()
}

func createWorkloadPriorityClasses(f *centralizedTASFixture) (high, low *kueue.WorkloadPriorityClass) {
	ginkgo.GinkgoHelper()
	high = utiltestingapi.MakeWorkloadPriorityClass("high-" + f.managerNs.Name).
		PriorityValue(100).
		Obj()
	low = utiltestingapi.MakeWorkloadPriorityClass("low-" + f.managerNs.Name).
		PriorityValue(10).
		Obj()
	for _, cl := range []client.Client{k8sManagerClient, k8sWorker1Client, k8sWorker2Client} {
		util.MustCreate(ctx, cl, high.DeepCopy())
		util.MustCreate(ctx, cl, low.DeepCopy())
	}
	return high, low
}

func waitForWorkloadOnCluster(wlKey types.NamespacedName, clusterName string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sManagerClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.ClusterName).NotTo(gomega.BeNil())
		g.Expect(*wl.Status.ClusterName).To(gomega.Equal(clusterName))
		cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
		g.Expect(cond).NotTo(gomega.BeNil())
		g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}

func workloadKeyForJob(job *batchv1.Job) types.NamespacedName {
	return types.NamespacedName{
		Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
		Namespace: job.Namespace,
	}
}

func waitForManagerCentralizedAdmission(wlKey types.NamespacedName) (clusterName string, levels []string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sManagerClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(wl.Status.ClusterName).NotTo(gomega.BeNil())
		g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		ta := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
		g.Expect(ta).NotTo(gomega.BeNil())
		g.Expect(ta.Levels).To(gomega.ContainElement(centralizedtas.ClusterLabel))
		g.Expect(ta.Levels).To(gomega.ContainElement(corev1.LabelHostname))
		g.Expect(wl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.BeNil())
		clusterName = *wl.Status.ClusterName
		levels = ta.Levels
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	return clusterName, levels
}

func waitForWorkloadAdmittedOnCluster(wlKey types.NamespacedName, expectedCluster string) {
	ginkgo.GinkgoHelper()
	waitForManagerCentralizedAdmission(wlKey)
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sManagerClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.ClusterName).NotTo(gomega.BeNil())
		g.Expect(*wl.Status.ClusterName).To(gomega.Equal(expectedCluster))
		cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
		g.Expect(cond).NotTo(gomega.BeNil())
		g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}

func expectWorkloadNotAdmitted(wlKey types.NamespacedName) {
	ginkgo.GinkgoHelper()
	gomega.Consistently(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sManagerClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
		if cond != nil {
			g.Expect(cond.Status).NotTo(gomega.Equal(metav1.ConditionTrue))
		}
	}, util.ShortConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
}

func expectWorkerPodsHostPinned(workerClient client.Client, ns, jobName, expectedHost string, count int) {
	ginkgo.GinkgoHelper()
	listOpts := util.GetListOptsFromLabel(fmt.Sprintf("%s=%s", batchv1.JobNameLabel, jobName))
	gomega.Eventually(func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		g.Expect(workerClient.List(ctx, pods, client.InNamespace(ns), listOpts)).To(gomega.Succeed())
		g.Expect(pods.Items).To(gomega.HaveLen(count))
		for _, pod := range pods.Items {
			g.Expect(pod.Spec.NodeSelector).To(gomega.HaveKeyWithValue(corev1.LabelHostname, expectedHost))
			g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
		}
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}

func terminateJobPods(f *centralizedTASFixture, clusterName, ns, jobName string, count int) {
	ginkgo.GinkgoHelper()
	wc := f.workers[clusterName]
	listOpts := util.GetListOptsFromLabel(fmt.Sprintf("%s=%s", batchv1.JobNameLabel, jobName))
	util.WaitForActivePodsAndTerminate(ctx, wc.client, wc.restClient, wc.cfg, ns, count, 0, listOpts)
}

func cleanupCentralizedTASFixture(f *centralizedTASFixture) {
	ginkgo.GinkgoHelper()
	gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, f.managerNs)).To(gomega.Succeed())
	gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, f.worker1Ns)).To(gomega.Succeed())
	gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, f.worker2Ns)).To(gomega.Succeed())

	for _, cq := range f.managerCQs {
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, &kueue.ClusterQueue{ObjectMeta: metav1.ObjectMeta{Name: cq.Name}}, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, &kueue.ClusterQueue{ObjectMeta: metav1.ObjectMeta{Name: cq.Name}}, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, cq, true, util.MediumTimeout)
	}
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, f.managerFlavor, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, f.managerTopology, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, f.worker1Flavor, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, f.worker1Topology, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, f.worker2Flavor, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, f.worker2Topology, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, f.multiKueueAc, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, f.multiKueueConfig, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, f.workerCluster1, true, util.MediumTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, f.workerCluster2, true, util.MediumTimeout)

	util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, f.managerNs)
	util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, f.worker1Ns)
	util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, f.worker2Ns)
}

func waitForClusterQueueWeightedShare(cqName string, cmp string, value int64) int64 {
	ginkgo.GinkgoHelper()
	var share int64
	gomega.Eventually(func(g gomega.Gomega) {
		cq := &kueue.ClusterQueue{}
		g.Expect(k8sManagerClient.Get(ctx, client.ObjectKey{Name: cqName}, cq)).To(gomega.Succeed())
		g.Expect(cq.Status.FairSharing).NotTo(gomega.BeNil())
		share = cq.Status.FairSharing.WeightedShare
		g.Expect(share).To(gomega.BeNumerically(cmp, value))
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	return share
}

func waitForJobManagedByMultiKueue(job *batchv1.Job) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		created := &batchv1.Job{}
		g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), created)).To(gomega.Succeed())
		g.Expect(ptr.Deref(created.Spec.ManagedBy, "")).To(gomega.BeEquivalentTo(kueue.MultiKueueControllerName))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func workerNsName(fixture *centralizedTASFixture, clusterName string) string {
	if clusterName == fixture.workerCluster1.Name {
		return fixture.worker1Ns.Name
	}
	return fixture.worker2Ns.Name
}

func hogTASNodeLeavingRoomFor(fixture *centralizedTASFixture, clusterName string, reserve resource.Quantity, hogName string) {
	ginkgo.GinkgoHelper()
	wc := fixture.workers[clusterName].client
	node := getTASWorkerNode(wc)
	hogCPU := cpuRequestToSaturateNode(node, reserve)
	createNonTASPodOnNode(wc, workerNsName(fixture, clusterName), hogName, node.Name, hogCPU)
}

func waitForWorkerPodsInNamespace(workerClient client.Client, ns string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		g.Expect(workerClient.List(ctx, pods, client.InNamespace(ns))).To(gomega.Succeed())
		g.Expect(pods.Items).NotTo(gomega.BeEmpty())
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}
