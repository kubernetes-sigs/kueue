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

package util

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/yaml"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta2"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	visibilityv1beta2 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	defaultE2eTestAgnHostImageOld = "registry.k8s.io/e2e-test-images/agnhost:2.52@sha256:b173c7d0ffe3d805d49f4dfe48375169b7b8d2e1feb81783efd61eb9d08042e6"

	defaultMetricsServiceName = "kueue-controller-manager-metrics-service"
)

func GetKueueNamespace() string {
	if ns := os.Getenv("KUEUE_NAMESPACE"); ns != "" {
		return ns
	}
	return configapi.DefaultNamespace
}

func GetAgnHostImageOld() string {
	if image := os.Getenv("E2E_TEST_AGNHOST_IMAGE_OLD"); image != "" {
		return image
	}

	return defaultE2eTestAgnHostImageOld
}

func GetAgnHostImage() string {
	if image := os.Getenv("E2E_TEST_AGNHOST_IMAGE"); image != "" {
		return image
	}

	agnhostDockerfilePath := filepath.Join(ProjectBaseDir, "hack", "agnhost", "Dockerfile")
	agnhostImage, err := getDockerImageFromDockerfile(agnhostDockerfilePath)
	if err != nil {
		panic(fmt.Errorf("failed to get agnhost image: %v", err))
	}

	return agnhostImage
}

func getDockerImageFromDockerfile(filePath string) (string, error) {
	// Open the Dockerfile
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open Dockerfile: %w", err)
	}
	defer file.Close()

	// Read the file line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines or comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for FROM instruction
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			// Extract the part after "FROM "
			parts := strings.Fields(line)
			if len(parts) < 2 {
				return "", fmt.Errorf("invalid FROM instruction: %s", line)
			}
			// The image name is the second field (parts[1])
			return parts[1], nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading Dockerfile: %w", err)
	}

	return "", errors.New("no FROM instruction found in Dockerfile")
}

func CreateClientUsingCluster(kContext string) (client.WithWatch, *rest.Config, error) {
	cfg, err := config.GetConfigWithContext(kContext)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get kubeconfig for context %q: %w", kContext, err)
	}
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	err = apiextensionsv1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kueue.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = cmv1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kueuev1beta1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = visibility.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = jobset.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kftraining.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kfmpi.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = leaderworkersetv1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	cfg.APIPath = "/api"
	cfg.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
	cfg.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	err = awv1beta2.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = rayv1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kftrainer.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = inventoryv1alpha1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	client, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	return client, cfg, nil
}

// CreateRestClient creates a *rest.RESTClient using the provided config.
func CreateRestClient(cfg *rest.Config) *rest.RESTClient {
	restClient, err := rest.RESTClientFor(cfg)
	gomega.ExpectWithOffset(1, err).Should(gomega.Succeed())
	gomega.ExpectWithOffset(1, restClient).NotTo(gomega.BeNil())

	return restClient
}

func CreateKueueClientset(user string) kueueclientset.Interface {
	ginkgo.GinkgoHelper()
	cfg, err := config.GetConfigWithContext("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfg).NotTo(gomega.BeNil())
	if user != "" {
		cfg.Impersonate.UserName = user
	}
	kueueClient, err := kueueclientset.NewForConfig(cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return kueueClient
}

func CreateVisibilityClient(user string) visibilityv1beta2.VisibilityV1beta2Interface {
	kueueClientset := CreateKueueClientset(user)
	return kueueClientset.VisibilityV1beta2()
}

func rolloutOperatorDeployment(ctx context.Context, k8sClient client.Client, key types.NamespacedName, kindClusterName string) {
	// Export logs before the rollout to preserve logs from the previous version.
	exportKindLogs(ctx, kindClusterName)

	deployment := &appsv1.Deployment{}
	var deploymentCondition *appsv1.DeploymentCondition

	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		deploymentCondition = FindDeploymentCondition(deployment, appsv1.DeploymentProgressing)
		g.Expect(deploymentCondition).NotTo(gomega.BeNil())
		g.Expect(deploymentCondition.Status).To(gomega.Equal(corev1.ConditionTrue))
		g.Expect(deploymentCondition.Reason).To(gomega.BeElementOf("NewReplicaSetAvailable", "ReplicaSetUpdated"))
	}, Timeout, Interval).Should(gomega.Succeed())
	beforeUpdateTime := deploymentCondition.LastUpdateTime

	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		deployment.Spec.Template.ObjectMeta.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
		g.Expect(k8sClient.Update(ctx, deployment)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())

	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		deploymentCondition := FindDeploymentCondition(deployment, appsv1.DeploymentProgressing)
		g.Expect(deploymentCondition).NotTo(gomega.BeNil())
		g.Expect(deploymentCondition.Status).To(gomega.Equal(corev1.ConditionTrue))
		g.Expect(deploymentCondition.Reason).To(gomega.Equal("NewReplicaSetAvailable"))
		afterUpdateTime := deploymentCondition.LastUpdateTime
		g.Expect(afterUpdateTime).NotTo(gomega.Equal(beforeUpdateTime))
	}, StartUpTimeout, Interval).Should(gomega.Succeed())
}

func exportKindLogs(ctx context.Context, kindClusterName string) {
	// Path to the kind binary
	kind := os.Getenv("KIND")
	// Path to the artifacts
	artifacts := os.Getenv("ARTIFACTS")

	if kind != "" && artifacts != "" {
		cmd := exec.CommandContext(ctx, kind, "export", "logs", "-n", kindClusterName, artifacts)
		cmd.Stdout = ginkgo.GinkgoWriter
		cmd.Stderr = ginkgo.GinkgoWriter
		gomega.Expect(cmd.Run()).To(gomega.Succeed())
	}
}

func waitForDeploymentAvailability(ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	deployment := &appsv1.Deployment{}
	waitForAvailableStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for availability of deployment: %q", key))
	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(deployment.Status.ObservedGeneration).To(gomega.Equal(deployment.Generation))
		g.Expect(deployment.Status.Replicas).To(gomega.Equal(*deployment.Spec.Replicas))
		g.Expect(deployment.Status.UpdatedReplicas).To(gomega.Equal(*deployment.Spec.Replicas))
		g.Expect(deployment.Status.AvailableReplicas).To(gomega.Equal(*deployment.Spec.Replicas))
		g.Expect(deployment.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
			appsv1.DeploymentCondition{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "Reason", "Message", "LastUpdateTime", "LastTransitionTime")),
		))
	}, StartUpTimeout, Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("Deployment is available in the cluster", "deployment", key, "waitingTime", time.Since(waitForAvailableStart))
}

func verifyNoControllerRestarts(ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	deployment := &appsv1.Deployment{}
	pods := &corev1.PodList{}
	waitForAvailableStart := time.Now()
	ginkgo.By(fmt.Sprintf("Checking no restarts for the controller: %q", key))
	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) error {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(k8sClient.List(ctx, pods, client.InNamespace(key.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
		for _, pod := range pods.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				// To make sure that we don't have restarts of controller-manager.
				// If we have that's mean that something went wrong, and there is
				// no needs to continue trying check availability.
				if cs.RestartCount > 0 {
					return gomega.StopTrying(fmt.Sprintf("%q in %q has restarted %d times", cs.Name, pod.Name, cs.RestartCount))
				}
			}
		}
		return nil
	}, StartUpTimeout, Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("No pods restart for the controller", "controller", key, "waitingTime", time.Since(waitForAvailableStart))
}

func WaitForKueueAvailability(ctx context.Context, k8sClient client.Client) {
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, kcmKey)
	verifyNoControllerRestarts(ctx, k8sClient, kcmKey)
}

func WaitForAppWrapperAvailability(ctx context.Context, k8sClient client.Client) {
	awmKey := types.NamespacedName{Namespace: "appwrapper-system", Name: "appwrapper-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, awmKey)
	verifyNoControllerRestarts(ctx, k8sClient, awmKey)
}

func WaitForJobSetAvailability(ctx context.Context, k8sClient client.Client) {
	jcmKey := types.NamespacedName{Namespace: "jobset-system", Name: "jobset-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, jcmKey)
	verifyNoControllerRestarts(ctx, k8sClient, jcmKey)
}

func WaitForLeaderWorkerSetAvailability(ctx context.Context, k8sClient client.Client) {
	jcmKey := types.NamespacedName{Namespace: "lws-system", Name: "lws-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, jcmKey)
	verifyNoControllerRestarts(ctx, k8sClient, jcmKey)
}

func WaitForKubeFlowTrainingOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "kubeflow", Name: "training-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kftoKey)
	verifyNoControllerRestarts(ctx, k8sClient, kftoKey)
}

func WaitForKubeFlowMPIOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "mpi-operator", Name: "mpi-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kftoKey)
	verifyNoControllerRestarts(ctx, k8sClient, kftoKey)
}

func WaitForKubeRayOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	// TODO: use ray-system namespace instead.
	// See discussions https://github.com/kubernetes-sigs/kueue/pull/4568#discussion_r2001045775 and
	// https://github.com/ray-project/kuberay/pull/2624/files#r2001143254 for context.
	kroKey := types.NamespacedName{Namespace: "default", Name: "kuberay-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kroKey)
	verifyNoControllerRestarts(ctx, k8sClient, kroKey)
}

func GetKueueConfiguration(ctx context.Context, k8sClient client.Client) *configapi.Configuration {
	var kueueCfg configapi.Configuration
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-manager-config"}
	configMap := &corev1.ConfigMap{}

	gomega.Expect(k8sClient.Get(ctx, kcmKey, configMap)).To(gomega.Succeed())
	gomega.Expect(yaml.Unmarshal([]byte(configMap.Data["controller_manager_config.yaml"]), &kueueCfg)).To(gomega.Succeed())
	return &kueueCfg
}

func ApplyKueueConfiguration(ctx context.Context, k8sClient client.Client, kueueCfg *configapi.Configuration) {
	configMap := &corev1.ConfigMap{}
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-manager-config"}
	config, err := yaml.Marshal(kueueCfg)

	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, kcmKey, configMap)).To(gomega.Succeed())
		configMap.Data["controller_manager_config.yaml"] = string(config)
		g.Expect(k8sClient.Update(ctx, configMap)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

func RestartKueueController(ctx context.Context, k8sClient client.Client, kindClusterName string) {
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
	restartStartTime := time.Now()
	rolloutOperatorDeployment(ctx, k8sClient, kcmKey, kindClusterName)
	WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)
	waitForDeploymentWithOnlyAvailableReplicas(ctx, k8sClient, kcmKey)
	WaitForLeaderElection(ctx, k8sClient, restartStartTime)
	waitForWebhookEndpointsReady(ctx, k8sClient)
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// waitForWebhookEndpointsReady waits for the webhook service EndpointSlice
// to contain the current controller pod IPs before making webhook requests.
func waitForWebhookEndpointsReady(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	kueueNS := GetKueueNamespace()

	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods,
			client.InNamespace(kueueNS),
			client.MatchingLabels{"control-plane": "controller-manager"},
		)).To(gomega.Succeed())

		podIPs := sets.New[string]()
		for _, pod := range pods.Items {
			if isPodReady(&pod) && pod.DeletionTimestamp == nil && pod.Status.PodIP != "" {
				podIPs.Insert(pod.Status.PodIP)
			}
		}
		g.Expect(podIPs.Len()).NotTo(gomega.BeZero(), "no ready controller pods")

		endpointSlices := &discoveryv1.EndpointSliceList{}
		g.Expect(k8sClient.List(ctx, endpointSlices,
			client.InNamespace(kueueNS),
			client.MatchingLabels{discoveryv1.LabelServiceName: "kueue-webhook-service"},
		)).To(gomega.Succeed())

		readyIPs := sets.New[string]()
		for _, slice := range endpointSlices.Items {
			for _, ep := range slice.Endpoints {
				if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
					readyIPs.Insert(ep.Addresses...)
				}
			}
		}

		g.Expect(readyIPs).To(gomega.Equal(podIPs))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func waitForDeploymentWithOnlyAvailableReplicas(ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	deployment := &appsv1.Deployment{}
	waitForAvailableStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for deployment to have only available replicas: %q", key))
	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(deployment.Status.Replicas).To(gomega.Equal(deployment.Status.AvailableReplicas))
	}, LongTimeout, Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("Deployment has only available replicas in the cluster", "deployment", key, "waitingTime", time.Since(waitForAvailableStart))
}

func WaitForActivePodsAndTerminate(ctx context.Context, k8sClient client.Client, restClient *rest.RESTClient, cfg *rest.Config, namespace string, activePodsCount, exitCode int, opts ...client.ListOption) {
	var activePods []corev1.Pod
	pods := corev1.PodList{}
	podListOpts := &client.ListOptions{}
	podListOpts.Namespace = namespace
	podListOpts.ApplyOptions(opts)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.List(ctx, &pods, podListOpts)).To(gomega.Succeed())
		activePods = make([]corev1.Pod, 0)
		for _, p := range pods.Items {
			if len(p.Status.PodIP) != 0 && p.Status.Phase == corev1.PodRunning {
				cmd := []string{"/bin/sh", "-c", fmt.Sprintf("curl \"http://%s:8080/readyz\"", p.Status.PodIP)}
				_, _, err := KExecute(ctx, cfg, restClient, namespace, p.Name, p.Spec.Containers[0].Name, cmd)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				activePods = append(activePods, p)
			}
		}
		g.Expect(activePods).To(gomega.HaveLen(activePodsCount))
	}, LongTimeout, Interval).Should(gomega.Succeed())

	for _, p := range activePods {
		ginkgo.GinkgoLogr.Info("Terminating pod", "pod", klog.KObj(&p))
		cmd := []string{"/bin/sh", "-c", fmt.Sprintf("curl \"http://%s:8080/exit?code=%v&timeout=2s&wait=2s\"", p.Status.PodIP, exitCode)}
		_, _, err := KExecute(ctx, cfg, restClient, namespace, p.Name, p.Spec.Containers[0].Name, cmd)
		// TODO: remove the custom handling of 137 response once this is fixed in the agnhost image
		// We add the custom handling to protect in situation when the target pods completes with the expected
		// exit code but it terminates before it completes sending the response.
		if err != nil {
			gomega.ExpectWithOffset(1, err.Error()).To(gomega.ContainSubstring("137"))
		} else {
			gomega.ExpectWithOffset(1, err).ToNot(gomega.HaveOccurred())
		}
	}
}

func WaitForKueueAvailabilityNoRestartCountCheck(ctx context.Context, k8sClient client.Client) {
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, kcmKey)
}

// WaitForLeaderElection waits for the kueue controller to acquire the leader lease
// after the given startTime to ensure the new controller has the lease.
func WaitForLeaderElection(ctx context.Context, k8sClient client.Client, startTime time.Time) {
	kueueNS := GetKueueNamespace()
	leaseKey := types.NamespacedName{Namespace: kueueNS, Name: configapi.DefaultLeaderElectionID}
	lease := &coordinationv1.Lease{}
	ginkgo.By(fmt.Sprintf("Waiting for leader election lease %q", leaseKey))
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, leaseKey, lease)).To(gomega.Succeed())
		g.Expect(lease.Spec.RenewTime).NotTo(gomega.BeNil())
		g.Expect(lease.Spec.RenewTime.After(startTime)).To(gomega.BeTrue())
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func WaitForKubeSystemControllersAvailability(ctx context.Context, k8sClient client.Client, clusterName string) {
	const ns = "kube-system"
	deployKey := types.NamespacedName{Namespace: ns, Name: "coredns"}
	ginkgo.By(fmt.Sprintf("Waiting for deployment %q to be available", deployKey.Name))
	waitForDeploymentAvailability(ctx, k8sClient, deployKey)

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
	}, StartUpTimeout, Interval).Should(gomega.Succeed())
}

func GetKuberayTestImage() string {
	kuberayTestImage, found := os.LookupEnv("KUBERAY_RAY_IMAGE")
	gomega.Expect(found).To(gomega.BeTrue())
	return kuberayTestImage
}

func CreateNamespaceWithLog(ctx context.Context, k8sClient client.Client, nsName string) *corev1.Namespace {
	ginkgo.GinkgoHelper()
	return CreateNamespaceFromObjectWithLog(ctx, k8sClient, utiltesting.MakeNamespace(nsName))
}

func CreateNamespaceFromPrefixWithLog(ctx context.Context, k8sClient client.Client, nsPrefix string) *corev1.Namespace {
	ginkgo.GinkgoHelper()
	return CreateNamespaceFromObjectWithLog(ctx, k8sClient, utiltesting.MakeNamespaceWithGenerateName(nsPrefix))
}

func CreateNamespaceFromObjectWithLog(ctx context.Context, k8sClient client.Client, ns *corev1.Namespace) *corev1.Namespace {
	MustCreate(ctx, k8sClient, ns)
	ginkgo.GinkgoLogr.Info("Created namespace", "namespace", ns.Name)
	return ns
}

func GetKueueMetrics(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string) (string, error) {
	kueueNS := GetKueueNamespace()
	metricsOutput, _, err := KExecute(ctx, cfg, restClient, kueueNS, curlPodName, curlContainerName, []string{
		"/bin/sh", "-c",
		fmt.Sprintf(
			"curl -s -k -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics",
			defaultMetricsServiceName, kueueNS,
		),
	})
	return string(metricsOutput), err
}

func ExpectMetricsToBeAvailable(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string, metrics [][]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, err := GetKueueMetrics(ctx, cfg, restClient, curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(metricsOutput).Should(utiltesting.ContainMetrics(metrics))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectMetricsNotToBeAvailable(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string, metrics [][]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, err := GetKueueMetrics(ctx, cfg, restClient, curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(metricsOutput).Should(utiltesting.ExcludeMetrics(metrics))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func WaitForPodRunning(ctx context.Context, k8sClient client.Client, pod *corev1.Pod) {
	ginkgo.GinkgoHelper()
	createdPod := &corev1.Pod{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
		g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func UpdateKueueConfiguration(ctx context.Context, k8sClient client.Client, config *configapi.Configuration, kindClusterName string, applyChanges func(cfg *configapi.Configuration)) {
	configurationUpdate := time.Now()
	config = config.DeepCopy()
	applyChanges(config)
	ApplyKueueConfiguration(ctx, k8sClient, config)
	RestartKueueController(ctx, k8sClient, kindClusterName)
	ginkgo.GinkgoLogr.Info("Kueue configuration updated", "took", time.Since(configurationUpdate))
}

func BaseSSAWorkload(w *kueue.Workload) *kueue.Workload {
	return workload.BaseSSAWorkload(w, true)
}
