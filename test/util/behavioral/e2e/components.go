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

package e2e

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	prometheusapi "github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

func ExpectNodeToBecomeReady(ctx context.Context, c client.Client, nodeName string, localQueue *kueue.LocalQueue) {
	ginkgo.GinkgoHelper()

	node := &corev1.Node{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKey{Name: nodeName}, node)).To(gomega.Succeed())
		g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg(fmt.Sprintf("Node %s did not become Ready", nodeName), node))

	waitForDummyWorkloadToRunOnNode(ctx, c, node, localQueue)
}

func waitForDummyWorkloadToRunOnNode(ctx context.Context, c client.Client, node *corev1.Node, lq *kueue.LocalQueue) {
	ginkgo.GinkgoHelper()

	ginkgo.By(fmt.Sprintf("Waiting for a dummy workload to run on the recovered node %s", node.Name), func() {
		dummyJob := testingjob.MakeJob(fmt.Sprintf("dummy-job-%s", node.Name), lq.Namespace).
			Queue(kueue.LocalQueueName(lq.Name)).
			NodeSelector(corev1.LabelHostname, node.Name).
			Image(GetAgnHostImage(), BehaviorExitFast).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			// we just need to test that the Node allows to run Pods already, using two Pods to indroduce extra redundancy
			Parallelism(2).
			Completions(2).
			CompletionMode(batchv1.IndexedCompletion).
			SuccessPolicy(&batchv1.SuccessPolicy{
				Rules: []batchv1.SuccessPolicyRule{
					{
						SucceededCount: new(int32(1)),
					},
				},
			}).
			Obj()

		behavioral.MustCreate(ctx, c, dummyJob)

		var createdDummyJob batchv1.Job
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(dummyJob), &createdDummyJob)).To(gomega.Succeed())
			g.Expect(createdDummyJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}, cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg(fmt.Sprintf("Dummy workload did not complete on node %s", node.Name), &createdDummyJob))
	})
}

func WaitForKueueAvailability(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	waitForKueueAvailability(ctx, k8sClient, true)
}

func WaitForKueueAvailabilityNoRestartCountCheck(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	waitForKueueAvailability(ctx, k8sClient, false)
}

func waitForDeploymentAvailability(ctx context.Context, k8sClient client.Client, key types.NamespacedName, checkNoRestarts bool) {
	ginkgo.GinkgoHelper()
	waitStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for availability of deployment: %q", key))
	gomega.Eventually(func(g gomega.Gomega) {
		deployment := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		desiredReplicas := ptr.Deref(deployment.Spec.Replicas, 0)
		g.Expect(deployment.Status.ObservedGeneration).To(gomega.Equal(deployment.Generation))
		g.Expect(deployment.Status.Replicas).To(gomega.Equal(desiredReplicas))
		g.Expect(deployment.Status.UpdatedReplicas).To(gomega.Equal(desiredReplicas))
		g.Expect(deployment.Status.AvailableReplicas).To(gomega.Equal(desiredReplicas))
		// For K8s 1.35+ with DeploymentReplicaSetTerminatingReplicas feature gate.
		// On older versions, TerminatingReplicas is nil, so this is always true.
		g.Expect(ptr.Deref(deployment.Status.TerminatingReplicas, 0)).To(gomega.BeZero(),
			"deployment still has terminating replicas")
		g.Expect(deployment.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
			appsv1.DeploymentCondition{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "Reason", "Message", "LastUpdateTime", "LastTransitionTime")),
		))

		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods,
			client.InNamespace(key.Namespace),
			client.MatchingLabelsSelector{Selector: selector},
		)).To(gomega.Succeed())
		if checkNoRestarts {
			ginkgo.By(fmt.Sprintf("Checking no restarts for the controller: %q", key))
			for _, pod := range pods.Items {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.RestartCount > 0 {
						gomega.StopTrying(fmt.Sprintf("%q in %q has restarted %d times", cs.Name, pod.Name, cs.RestartCount)).Now()
					}
				}
			}
		} else {
			for _, pod := range pods.Items {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.RestartCount > 0 {
						ginkgo.GinkgoLogr.Info("Container restarted (tolerated)", "deployment", key, "pod", pod.Name, "container", cs.Name, "restartCount", cs.RestartCount)
					}
				}
			}
		}
	}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("Deployment is available", "deployment", key, "noRestarts", checkNoRestarts, "waitingTime", time.Since(waitStart))
}

func WaitForAppWrapperAvailability(ctx context.Context, k8sClient client.Client) {
	awmKey := types.NamespacedName{Namespace: "appwrapper-system", Name: "appwrapper-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, awmKey, true)
}

func WaitForJobSetAvailability(ctx context.Context, k8sClient client.Client) {
	jcmKey := types.NamespacedName{Namespace: "jobset-system", Name: "jobset-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, jcmKey, true)
}

func WaitForLeaderWorkerSetAvailability(ctx context.Context, k8sClient client.Client) {
	jcmKey := types.NamespacedName{Namespace: "lws-system", Name: "lws-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, jcmKey, true)
}

func WaitForKubeFlowTrainingOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "kubeflow", Name: "training-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kftoKey, true)
}

func WaitForSparkOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	sparkctrKey := types.NamespacedName{Namespace: "spark-operator", Name: "spark-operator-controller"}
	waitForDeploymentAvailability(ctx, k8sClient, sparkctrKey, true)
}

func WaitForKubeFlowMPIOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "mpi-operator", Name: "mpi-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kftoKey, true)
}

func WaitForKubeRayOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	// TODO: use ray-system namespace instead.
	// See discussions https://github.com/kubernetes-sigs/kueue/pull/4568#discussion_r2001045775 and
	// https://github.com/ray-project/kuberay/pull/2624/files#r2001143254 for context.
	kroKey := types.NamespacedName{Namespace: "default", Name: "kuberay-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kroKey, true)
}

func WaitForKubeFlowTrainnerControllerManagerAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "kubeflow-system", Name: "kubeflow-trainer-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, kftoKey, true)
}

func waitForKueueAvailability(ctx context.Context, k8sClient client.Client, checkNoRestarts bool) {
	ginkgo.GinkgoHelper()
	kcmKey := types.NamespacedName{Namespace: GetKueueNamespace(), Name: "kueue-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, kcmKey, checkNoRestarts)
	waitForKueueControllerReadyWithWebhookEndpoints(ctx, k8sClient, kcmKey)
	waitForLeaderElection(ctx, k8sClient)
}

// waitForLeaderElection waits for the kueue controller to acquire the leader lease
func waitForLeaderElection(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	kueueNS := GetKueueNamespace()
	leaseKey := types.NamespacedName{Namespace: kueueNS, Name: configapi.DefaultLeaderElectionID}
	lease := &coordinationv1.Lease{}
	startTime := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for leader election lease %q", leaseKey))
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, leaseKey, lease)).To(gomega.Succeed())
		g.Expect(lease.Spec.RenewTime).NotTo(gomega.BeNil())
		g.Expect(lease.Spec.RenewTime.After(startTime)).To(gomega.BeTrue())
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
}

func waitForKueueControllerReadyWithWebhookEndpoints(ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	waitStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for ready pods and webhook endpoints: %q", key))

	gomega.Eventually(func(g gomega.Gomega) {
		deployment := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		desiredReplicas := ptr.Deref(deployment.Spec.Replicas, 0)

		g.Expect(deployment.Status.AvailableReplicas).To(gomega.Equal(desiredReplicas),
			fmt.Sprintf("available replicas: %d, desired: %d", deployment.Status.AvailableReplicas, desiredReplicas))

		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods,
			client.InNamespace(key.Namespace),
			client.MatchingLabelsSelector{Selector: selector},
		)).To(gomega.Succeed())

		readyPodIPs := sets.New[string]()
		for _, pod := range pods.Items {
			if isPodReady(&pod) && pod.DeletionTimestamp == nil && pod.Status.PodIP != "" {
				readyPodIPs.Insert(pod.Status.PodIP)
			}
		}
		g.Expect(readyPodIPs).To(gomega.HaveLen(int(desiredReplicas)),
			fmt.Sprintf("ready pods: %d, desired: %d", readyPodIPs.Len(), desiredReplicas))

		endpointSlices := &discoveryv1.EndpointSliceList{}
		g.Expect(k8sClient.List(ctx, endpointSlices,
			client.InNamespace(key.Namespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: "kueue-webhook-service"},
		)).To(gomega.Succeed())

		endpointIPs := sets.New[string]()
		for _, slice := range endpointSlices.Items {
			for _, ep := range slice.Endpoints {
				if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
					endpointIPs.Insert(ep.Addresses...)
				}
			}
		}
		g.Expect(endpointIPs).To(gomega.Equal(readyPodIPs))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

	// EndpointSlice readiness does not guarantee that the apiserver has dropped
	// stale webhook connections, so verify the webhook path itself.
	ginkgo.By(fmt.Sprintf("Probing the webhook data path: %q", key))
	gomega.Eventually(func(g gomega.Gomega) {
		probeRF := &kueue.ResourceFlavor{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "webhook-probe-"},
		}
		g.Expect(k8sClient.Create(ctx, probeRF, client.DryRunAll)).To(gomega.Succeed())
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

	ginkgo.GinkgoLogr.Info("Ready pods and webhook endpoints verified", "deployment", key, "waitingTime", time.Since(waitStart))
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func WaitForPrometheusAvailability(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	key := types.NamespacedName{Namespace: "monitoring", Name: "prometheus-prometheus"}
	ginkgo.By(fmt.Sprintf("Waiting for availability of StatefulSet: %q", key))
	gomega.Eventually(func(g gomega.Gomega) {
		sts := &appsv1.StatefulSet{}
		g.Expect(k8sClient.Get(ctx, key, sts)).To(gomega.Succeed())
		desiredReplicas := ptr.Deref(sts.Spec.Replicas, 1)
		g.Expect(sts.Status.ReadyReplicas).To(gomega.Equal(desiredReplicas))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
}

func CreatePrometheusClient(cfg *rest.Config) prometheusv1.API {
	ginkgo.GinkgoHelper()
	transport, err := rest.TransportFor(cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	client, err := prometheusapi.NewClient(prometheusapi.Config{
		Address:      fmt.Sprintf("%s/api/v1/namespaces/monitoring/services/prometheus-api:web/proxy", cfg.Host),
		RoundTripper: transport,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	return prometheusv1.NewAPI(client)
}

func WaitForKubeSystemControllersAvailability(ctx context.Context, k8sClient client.Client, clusterName string) {
	const ns = "kube-system"
	deployKey := types.NamespacedName{Namespace: ns, Name: "coredns"}
	ginkgo.By(fmt.Sprintf("Waiting for deployment %q to be available", deployKey.Name))
	waitForDeploymentAvailability(ctx, k8sClient, deployKey, false)

	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		// we wait for all the DaemonSets and Pods in kube-system to be available at the same time
		for _, dsName := range []string{
			"kindnet",
			"kube-proxy",
		} {
			ginkgo.GinkgoLogr.Info(fmt.Sprintf("Checking if daemonset %q to be available", dsName))
			dsKey := types.NamespacedName{Namespace: ns, Name: dsName}
			daemonset := &appsv1.DaemonSet{}
			g.Expect(k8sClient.Get(ctx, dsKey, daemonset)).To(gomega.Succeed())
			g.Expect(daemonset.Status.DesiredNumberScheduled).To(gomega.Equal(daemonset.Status.NumberAvailable))
		}

		for _, podName := range []string{
			"etcd",
			"kube-controller-manager",
			"kube-apiserver",
			"kube-scheduler",
		} {
			ginkgo.GinkgoLogr.Info(fmt.Sprintf("Checking if pod %q to be available", podName))
			pod := &corev1.Pod{}
			podKey := types.NamespacedName{Namespace: ns, Name: fmt.Sprintf("%s-%s", podName, clusterName)}
			g.Expect(k8sClient.Get(ctx, podKey, pod)).To(gomega.Succeed())
			g.Expect(pod.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(corev1.PodCondition{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}, cmpopts.IgnoreFields(corev1.PodCondition{}, "Reason", "LastTransitionTime", "LastProbeTime"))))
		}
	}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
}

func WaitForPodRunning(ctx context.Context, k8sClient client.Client, pod *corev1.Pod) {
	ginkgo.GinkgoHelper()
	createdPod := &corev1.Pod{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
		g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
}
