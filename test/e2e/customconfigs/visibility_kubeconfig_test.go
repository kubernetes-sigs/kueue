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

package customconfigse2e

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	kueueNamespace   = "kueue-system"
	kueueManagerName = "kueue-controller-manager"
	configMapName    = "visibility-kubeconfig-test"
	customSAName     = "visibility-test-sa"
	customSecretName = "visibility-test-sa-token"
)

// This kubeconfig uses the token of our custom ServiceAccount mounted at /etc/custom-sa/token.
const rbacKubeConfig = `
apiVersion: v1
kind: Config
clusters:
- name: local
  cluster:
    server: https://kubernetes.default.svc
    certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
users:
- name: custom-sa
  user:
    tokenFile: /etc/custom-sa/token
contexts:
- name: default
  context:
    cluster: local
    user: custom-sa
current-context: default
`

var _ = ginkgo.Describe("Visibility Server KubeConfig flag with RBAC", func() {
	var ctx context.Context
	var originalDeployment appsv1.Deployment

	ginkgo.BeforeEach(func() {
		ctx = context.Background()

		err := k8sClient.Get(ctx, types.NamespacedName{Name: kueueManagerName, Namespace: kueueNamespace}, &originalDeployment)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Create Custom ServiceAccount
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: customSAName, Namespace: kueueNamespace}}
		gomega.Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, sa))).To(gomega.Succeed())

		// Create a token Secret for the custom SA
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        customSecretName,
				Namespace:   kueueNamespace,
				Annotations: map[string]string{corev1.ServiceAccountNameKey: customSAName},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		}
		gomega.Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, secret))).To(gomega.Succeed())

		// Create the ConfigMap for the KubeConfig
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: kueueNamespace},
			Data:       map[string]string{"config": rbacKubeConfig},
		}
		gomega.Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, cm))).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		// Restore the original deployment
		gomega.Expect(k8sClient.Update(ctx, &originalDeployment)).To(gomega.Succeed())
		waitForDeployment(ctx, kueueManagerName, kueueNamespace)

		// Clean up created resources
		_ = k8sClient.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: kueueNamespace}})
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: customSecretName, Namespace: kueueNamespace}})
		_ = k8sClient.Delete(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: customSAName, Namespace: kueueNamespace}})
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-delegator"}})
		_ = k8sClient.Delete(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-reader", Namespace: "kube-system"}})
	})

	ginkgo.It("Should use the RBAC identity from the provided kubeconfig", func() {
		patchedDeployment := originalDeployment.DeepCopy()

		// Mount the ConfigMap and the Custom SA Token Secret
		patchedDeployment.Spec.Template.Spec.Volumes = append(patchedDeployment.Spec.Template.Spec.Volumes,
			corev1.Volume{
				Name:         "kubeconfig-vol",
				VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMapName}}},
			},
			corev1.Volume{
				Name:         "custom-sa-vol",
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: customSecretName}},
			},
		)

		for i, c := range patchedDeployment.Spec.Template.Spec.Containers {
			if c.Name == "manager" {
				patchedDeployment.Spec.Template.Spec.Containers[i].VolumeMounts = append(c.VolumeMounts,
					corev1.VolumeMount{Name: "kubeconfig-vol", MountPath: "/etc/kubeconfig", ReadOnly: true},
					corev1.VolumeMount{Name: "custom-sa-vol", MountPath: "/etc/custom-sa", ReadOnly: true},
				)
				patchedDeployment.Spec.Template.Spec.Containers[i].Args = append(c.Args, "--kubeconfig=/etc/kubeconfig/config")
			}
		}

		gomega.Expect(k8sClient.Update(ctx, patchedDeployment)).To(gomega.Succeed())
		waitForDeployment(ctx, kueueManagerName, kueueNamespace)

		cqName := "test-kubeconfig-cq"
		cq := &kueue.ClusterQueue{
			ObjectMeta: metav1.ObjectMeta{Name: cqName},
			Spec:       kueue.ClusterQueueSpec{CohortName: "test-cohort"},
		}
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
		defer k8sClient.Delete(ctx, cq)

		// NEGATIVE TEST: The custom SA lacks 'system:auth-delegator'. The visibility server
		// is forced to use it, so it cannot perform SubjectAccessReviews.
		ginkgo.By("Expecting API requests to fail due to lack of auth delegation permissions")
		gomega.Eventually(func(g gomega.Gomega) {
			var visCQ visibility.ClusterQueue
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cqName}, &visCQ)
			g.Expect(err).To(gomega.HaveOccurred())
			// Generally surfaces as InternalError when SAR fails
			g.Expect(k8serrors.IsInternalError(err) || k8serrors.IsServiceUnavailable(err)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// POSITIVE TEST: Grant the required permissions to our custom ServiceAccount
		ginkgo.By("Granting permissions to the custom SA and verifying requests succeed")
		authDelegatorBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-delegator"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:auth-delegator"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNamespace}},
		}
		gomega.Expect(k8sClient.Create(ctx, authDelegatorBinding)).To(gomega.Succeed())

		authReaderBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-reader", Namespace: "kube-system"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "extension-apiserver-authentication-reader"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNamespace}},
		}
		gomega.Expect(k8sClient.Create(ctx, authReaderBinding)).To(gomega.Succeed())

		// Now the requests should pass successfully
		gomega.Eventually(func(g gomega.Gomega) {
			var visCQ visibility.ClusterQueue
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cqName}, &visCQ)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(visCQ.Name).To(gomega.Equal(cqName))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})

func waitForDeployment(ctx context.Context, name, namespace string) {
	gomega.Eventually(func(g gomega.Gomega) {
		var d appsv1.Deployment
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &d)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(d.Status.UpdatedReplicas).To(gomega.Equal(d.Status.Replicas))
		g.Expect(d.Status.ReadyReplicas).To(gomega.Equal(d.Status.Replicas))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}
