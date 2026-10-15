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

package certmanager

import (
	"fmt"
	"net"
	"os"
	"slices"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// Serial isolates shared configuration changes; priority also puts this spec last in single-process runs.
var _ = ginkgo.Describe("Metrics access control", ginkgo.Serial, ginkgo.SpecPriority(-1), ginkgo.Label("feature:metrics"), func() {
	ginkgo.It("should allow verified HTTPS scraping from a sidecar without a token and refuse the pod IP", func() {
		deploymentKey := client.ObjectKey{Namespace: kueueNS, Name: "kueue-controller-manager"}
		originalDeployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, deploymentKey, originalDeployment)).To(gomega.Succeed())
		originalConfig := e2e.GetKueueConfiguration(ctx, k8sClient)
		gomega.Expect(originalConfig.InternalCertManagement).NotTo(gomega.BeNil())
		gomega.Expect(originalConfig.InternalCertManagement.Enable).To(gomega.HaveValue(gomega.BeFalse()))
		clusterName := os.Getenv("KIND_CLUSTER_NAME")

		curlPod := testingjobspod.MakePod("curl-loopback-metrics", kueueNS).
			ServiceAccountName(serviceAccountName).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).Obj()
		curlPod.Spec.Volumes = []corev1.Volume{{Name: "metrics-certs", Secret: &corev1.SecretVolumeSource{SecretName: certSecretName}}}
		curlPod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "metrics-certs", MountPath: certMountPath, ReadOnly: true}}
		// Copy the sidecar before Pod admission adds a token volume mount.
		sidecar := *curlPod.Spec.Containers[0].DeepCopy()
		sidecar.Name = "metrics-scraper"
		sidecar.SecurityContext = &corev1.SecurityContext{RunAsUser: new(int64(65532))}
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, behavioral.LongTimeout)
		})
		behavioral.MustCreate(ctx, k8sClient, curlPod)
		e2e.WaitForPodRunning(ctx, k8sClient, curlPod)
		curlContainer := curlPod.Spec.Containers[0].Name

		binding := &rbacv1.ClusterRoleBinding{
			Name:     "loopback-metrics-reader",
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccountName, Namespace: kueueNS}},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: metricsReaderClusterRoleName},
		}
		ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, binding, true) })
		behavioral.MustCreate(ctx, k8sClient, binding)

		// Register restoration before changing either shared resource. LIFO cleanup keeps
		// the authenticated scraper and its RBAC available until restoration is verified.
		ginkgo.DeferCleanup(func() {
			e2e.UpdateKueueConfiguration(ctx, k8sClient, originalConfig)
			e2e.UpdateDeploymentAndWaitForProgressing(ctx, k8sClient, deploymentKey, clusterName, func(deployment *appsv1.Deployment) {
				deployment.Spec = *originalDeployment.Spec.DeepCopy()
				if deployment.Spec.Template.Annotations == nil {
					deployment.Spec.Template.Annotations = make(map[string]string)
				}
				deployment.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339Nano)
			})
			e2e.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)
			expectMetricsToBeAvailable(curlPod.Name, curlContainer, [][]string{{"go_gc_duration_seconds"}})
		})

		ginkgo.By("enabling loopback metrics with external serving certificates")
		e2e.UpdateKueueConfiguration(ctx, k8sClient, originalConfig, func(configuration *configapi.Configuration) {
			if configuration.FeatureGates == nil {
				configuration.FeatureGates = make(map[string]bool)
			}
			configuration.FeatureGates["MetricsAccessControl"] = true
			configuration.Metrics.AccessControlMode = new(configapi.MetricsAccessControlModeNone)
			configuration.Metrics.BindAddress = "127.0.0.1:8443"
		})
		e2e.UpdateDeploymentAndWaitForProgressing(ctx, k8sClient, deploymentKey, clusterName, func(deployment *appsv1.Deployment) {
			deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, sidecar)
			if !slices.ContainsFunc(deployment.Spec.Template.Spec.Volumes, func(volume corev1.Volume) bool { return volume.Name == "metrics-certs" }) {
				deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, curlPod.Spec.Volumes...)
			}
		})
		e2e.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		serverName := fmt.Sprintf("%s.%s.svc.cluster.local", metricsServiceName, kueueNS)
		var pods corev1.PodList
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(kueueNS), client.MatchingLabels(originalDeployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(int(*originalDeployment.Spec.Replicas)))
			for _, pod := range pods.Items {
				g.Expect(pod.DeletionTimestamp).To(gomega.BeNil())
				g.Expect(slices.ContainsFunc(pod.Spec.Containers, func(container corev1.Container) bool { return container.Name == sidecar.Name })).To(gomega.BeTrue())
			}
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

		for _, pod := range pods.Items {
			ginkgo.By("scraping over verified HTTPS without an Authorization header from " + pod.Name)
			gomega.Eventually(func(g gomega.Gomega) {
				output, _, err := e2e.KExecute(ctx, cfg, restClient, kueueNS, pod.Name, sidecar.Name, []string{
					"curl", "--silent", "--show-error", "--fail", "--noproxy", "*", "--connect-timeout", "5", "--max-time", "15",
					"--cacert", certMountPath + "/ca.crt", "--resolve", serverName + ":8443:127.0.0.1", "https://" + serverName + ":8443/metrics",
				})
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(string(output)).To(gomega.ContainSubstring("go_gc_duration_seconds"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("confirming the manager pod IP refuses metrics connections")
			_, _, err := e2e.KExecute(ctx, cfg, restClient, kueueNS, curlPod.Name, curlContainer, []string{
				"/bin/sh", "-c", `curl --silent --show-error --noproxy '*' --connect-timeout 5 --max-time 15 "$1"; result=$?; test "$result" -eq 7`,
				"sh", "https://" + net.JoinHostPort(pod.Status.PodIP, "8443") + "/metrics",
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "curl must report connection refused (exit 7), rather than TLS or authentication failure")
		}
	})
})
