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

package upgrade

import (
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Upgrade Validation", ginkgo.Ordered, func() {
	ginkgo.It("should have completed upgrade successfully", func() {
		ginkgo.By("Waiting for conversion webhook to be ready to list LocalQueues")
		gomega.Eventually(func(g gomega.Gomega) {
			lqList := &kueue.LocalQueueList{}
			g.Expect(k8sClient.List(ctx, lqList)).To(gomega.Succeed(), "Should be able to list LocalQueues (conversion webhook should be ready)")
			g.Expect(lqList.Items).NotTo(gomega.BeEmpty(), "Should have at least one LocalQueue")
		}, util.StartUpTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Waiting for mutating webhook to be ready")
		gomega.Eventually(func(g gomega.Gomega) {
			mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "kueue-mutating-webhook-configuration"}, mwc)).To(gomega.Succeed())

			for _, webhook := range mwc.Webhooks {
				g.Expect(webhook.ClientConfig.CABundle).ToNot(gomega.BeEmpty())
			}
		}, util.StartUpTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should have CA bundles injected in all CRD conversion webhooks", func() {
		crdList := &apiextensionsv1.CustomResourceDefinitionList{}
		gomega.Expect(k8sClient.List(ctx, crdList)).To(gomega.Succeed())

		for _, crd := range crdList.Items {
			if !strings.HasPrefix(crd.Name, "kueue.x-k8s.io") {
				continue
			}
			if crd.Spec.Conversion == nil || crd.Spec.Conversion.Strategy != apiextensionsv1.WebhookConverter {
				continue
			}
			if crd.Spec.Conversion.Webhook == nil || crd.Spec.Conversion.Webhook.ClientConfig == nil {
				continue
			}

			caBundle := crd.Spec.Conversion.Webhook.ClientConfig.CABundle
			gomega.Expect(caBundle).NotTo(gomega.BeEmpty(), "CA bundle should not be empty for CRD %s", crd.Name)
			gomega.Expect(string(caBundle)).To(gomega.ContainSubstring("-----BEGIN CERTIFICATE-----"),
				"CA bundle should contain a valid PEM certificate for CRD %s", crd.Name)
		}
	})

	ginkgo.It("should read pre-existing LocalQueue without x509 errors", func() {
		lq := &kueue.LocalQueue{}
		lqKey := types.NamespacedName{Name: "upgrade-test-lq", Namespace: "kueue-upgrade-test"}
		gomega.Expect(k8sClient.Get(ctx, lqKey, lq)).To(gomega.Succeed())
	})

	ginkgo.It("should read pre-existing ClusterQueue without x509 errors", func() {
		cq := &kueue.ClusterQueue{}
		cqKey := types.NamespacedName{Name: "upgrade-test-cq"}
		gomega.Expect(k8sClient.Get(ctx, cqKey, cq)).To(gomega.Succeed())
	})

	ginkgo.It("should have webhook secret with valid certificates", func() {
		secret := &corev1.Secret{}
		secretKey := types.NamespacedName{Name: "kueue-webhook-server-cert", Namespace: kueueNS}
		gomega.Expect(k8sClient.Get(ctx, secretKey, secret)).To(gomega.Succeed())

		gomega.Expect(secret.Data).To(gomega.HaveKey("tls.crt"))
		gomega.Expect(secret.Data).To(gomega.HaveKey("tls.key"))
		gomega.Expect(secret.Data["tls.crt"]).NotTo(gomega.BeEmpty())
		gomega.Expect(secret.Data["tls.key"]).NotTo(gomega.BeEmpty())
	})

	ginkgo.It("should have healthy controller manager pods", func() {
		podList := &corev1.PodList{}
		listOpts := []client.ListOption{
			client.InNamespace(kueueNS),
			client.MatchingLabels{"control-plane": "controller-manager"},
		}
		gomega.Expect(k8sClient.List(ctx, podList, listOpts...)).To(gomega.Succeed())
		gomega.Expect(podList.Items).NotTo(gomega.BeEmpty())

		for _, pod := range podList.Items {
			gomega.Expect(pod.Status.ContainerStatuses).NotTo(gomega.BeEmpty())
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Name == "manager" {
					gomega.Expect(containerStatus.Ready).To(gomega.BeTrue(),
						"Container %s in pod %s should be ready", containerStatus.Name, pod.Name)
					ginkgo.GinkgoLogr.Info("Pod health check",
						"pod", pod.Name,
						"container", containerStatus.Name,
						"restarts", containerStatus.RestartCount,
						"ready", containerStatus.Ready)
				}
			}
		}
	})

	ginkgo.It("should admit jobs using pre-existing LocalQueue", func() {
		const jobNamespace = "kueue-upgrade-test"
		const queueName = "upgrade-test-lq"

		testJob := testingjob.MakeJob("upgrade-validation-job", jobNamespace).
			Queue(kueue.LocalQueueName(queueName)).
			Image(util.GetAgnHostImage(), util.BehaviorExitFast).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			RequestAndLimit(corev1.ResourceMemory, "50Mi").
			Obj()

		ginkgo.By("Creating test job")
		util.MustCreate(ctx, k8sClient, testJob)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, testJob)).To(gomega.Succeed())
		})

		ginkgo.GinkgoLogr.Info("Created test job", "job", testJob.Name, "queue", queueName)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(testJob.Name, testJob.UID),
			Namespace: jobNamespace,
		}

		ginkgo.By("Waiting for workload to be created")
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Waiting for job to be admitted")
		util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(testJob), nil)

		ginkgo.By("Verifying workload is admitted")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
	})
})
