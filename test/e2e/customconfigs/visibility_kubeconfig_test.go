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
	"k8s.io/client-go/kubernetes/scheme"
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
	testLabelKey     = "visibility-test-rbac"
	testLabelValue   = "true"
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

func init() {
	_ = visibility.AddToScheme(scheme.Scheme)
}

var _ = ginkgo.Describe("Visibility Server KubeConfig flag with RBAC", func() {
	var ctx context.Context
	var originalDeployment appsv1.Deployment

	ginkgo.BeforeEach(func() {
		ctx = context.Background()

		err := k8sClient.Get(ctx, types.NamespacedName{Name: kueueManagerName, Namespace: kueueNamespace}, &originalDeployment)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Create Custom ServiceAccount
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: customSAName, Namespace: kueueNamespace}}
		util.MustCreate(ctx, k8sClient, sa)

		// Create a token Secret for the custom SA
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        customSecretName,
				Namespace:   kueueNamespace,
				Annotations: map[string]string{corev1.ServiceAccountNameKey: customSAName},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		}
		util.MustCreate(ctx, k8sClient, secret)

		// Create the ConfigMap for the KubeConfig
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: kueueNamespace},
			Data:       map[string]string{"config": rbacKubeConfig},
		}
		util.MustCreate(ctx, k8sClient, cm)

		// Clone all base permissions to our custom SA so the main controller can boot,
		// explicitly holding back the auth delegation permission for our negative test.
		cloneControllerRBAC(ctx)

		// Create the auth-reader binding so the visibility server can start up.
		authReaderBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-reader", Namespace: "kube-system"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "extension-apiserver-authentication-reader"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNamespace}},
		}
		util.MustCreate(ctx, k8sClient, authReaderBinding)
	})

	ginkgo.AfterEach(func() {
		// Restore the original deployment
		gomega.Expect(k8sClient.Update(ctx, &originalDeployment)).To(gomega.Succeed())
		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		// Clean up created resources
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: kueueNamespace}}, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: customSecretName, Namespace: kueueNamespace}}, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: customSAName, Namespace: kueueNamespace}}, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-delegator"}}, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-reader", Namespace: "kube-system"}}, true)

		// Clean up our dynamically cloned roles
		_ = k8sClient.DeleteAllOf(ctx, &rbacv1.ClusterRoleBinding{}, client.MatchingLabels{testLabelKey: testLabelValue})
		_ = k8sClient.DeleteAllOf(ctx, &rbacv1.RoleBinding{}, client.InNamespace(kueueNamespace), client.MatchingLabels{testLabelKey: testLabelValue})
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
		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		cqName := "test-kubeconfig-cq"
		cq := &kueue.ClusterQueue{
			ObjectMeta: metav1.ObjectMeta{Name: cqName},
			Spec:       kueue.ClusterQueueSpec{CohortName: "test-cohort"},
		}
		util.MustCreate(ctx, k8sClient, cq)
		defer util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)

		// NEGATIVE TEST: The custom SA lacks 'system:auth-delegator'. The visibility server
		// is forced to use it, so it cannot perform SubjectAccessReviews.
		ginkgo.By("Expecting API requests to fail due to lack of auth delegation permissions")
		gomega.Eventually(func(g gomega.Gomega) {
			var visCQ visibility.ClusterQueue
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cqName}, &visCQ)
			g.Expect(err).To(gomega.HaveOccurred())

			// Check for standard auth failures (401/403) or generic server errors (500/503)
			isExpectedError := k8serrors.IsUnauthorized(err) ||
				k8serrors.IsForbidden(err) ||
				k8serrors.IsInternalError(err) ||
				k8serrors.IsServiceUnavailable(err)

			g.Expect(isExpectedError).To(gomega.BeTrue(), "Expected an auth or internal error, but got: %v", err)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// POSITIVE TEST: Grant the required permissions to our custom ServiceAccount
		ginkgo.By("Granting permissions to the custom SA and verifying requests succeed")
		authDelegatorBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "visibility-test-auth-delegator"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:auth-delegator"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNamespace}},
		}
		util.MustCreate(ctx, k8sClient, authDelegatorBinding)

		ginkgo.By("Now the requests should pass successfully")
		gomega.Eventually(func(g gomega.Gomega) {
			var visCQ visibility.ClusterQueue
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cqName}, &visCQ)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(visCQ.Name).To(gomega.Equal(cqName))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})

// cloneControllerRBAC dynamically copies all RoleBindings and ClusterRoleBindings
// from the default Kueue SA to our custom SA, explicitly omitting system:auth-delegator.
func cloneControllerRBAC(ctx context.Context) {
	// Clone ClusterRoleBindings
	var crbs rbacv1.ClusterRoleBindingList
	gomega.Expect(k8sClient.List(ctx, &crbs)).To(gomega.Succeed())
	for _, crb := range crbs.Items {
		for _, sub := range crb.Subjects {
			if sub.Kind == "ServiceAccount" && sub.Name == "kueue-controller-manager" && sub.Namespace == kueueNamespace {
				// We intentionally skip this so the negative test catches the missing permission
				if crb.RoleRef.Name == "system:auth-delegator" {
					continue
				}
				newCRB := &rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "vistest-copy-" + crb.Name,
						Labels: map[string]string{testLabelKey: testLabelValue},
					},
					RoleRef:  crb.RoleRef,
					Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNamespace}},
				}
				_ = client.IgnoreAlreadyExists(k8sClient.Create(ctx, newCRB))
			}
		}
	}

	// Clone RoleBindings (mostly for leader election in the kueue-system namespace)
	var rbs rbacv1.RoleBindingList
	gomega.Expect(k8sClient.List(ctx, &rbs, client.InNamespace(kueueNamespace))).To(gomega.Succeed())
	for _, rb := range rbs.Items {
		for _, sub := range rb.Subjects {
			if sub.Kind == "ServiceAccount" && sub.Name == "kueue-controller-manager" && sub.Namespace == kueueNamespace {
				newRB := &rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vistest-copy-" + rb.Name,
						Namespace: kueueNamespace,
						Labels:    map[string]string{testLabelKey: testLabelValue},
					},
					RoleRef:  rb.RoleRef,
					Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNamespace}},
				}
				_ = client.IgnoreAlreadyExists(k8sClient.Create(ctx, newRB))
			}
		}
	}
}
