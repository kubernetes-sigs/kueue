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
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	kubeSystemNamespace = "kube-system"
	kueueManagerName    = "kueue-controller-manager"
	configMapName       = "visibility-kubeconfig-test"
	customSAName        = "visibility-test-sa"
	customSecretName    = "visibility-test-sa-token"
	testLabelKey        = "visibility-test-rbac"
	testLabelValue      = "true"
	kubeconfigVolName   = "kubeconfig-vol"
	customSAVolName     = "custom-sa-vol"
	customSAMountPath   = "/etc/custom-sa"
	cqName              = "test-kubeconfig-cq"
)

var _ = ginkgo.Describe("Visibility Server KubeConfig flag with RBAC", func() {
	var originalDeployment appsv1.Deployment

	ginkgo.BeforeEach(func() {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: kueueManagerName, Namespace: kueueNS}, &originalDeployment)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Creating Custom ServiceAccount")
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: customSAName, Namespace: kueueNS}}
		util.MustCreate(ctx, k8sClient, sa)

		ginkgo.By("Creating a token Secret for the custom SA")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        customSecretName,
				Namespace:   kueueNS,
				Annotations: map[string]string{corev1.ServiceAccountNameKey: customSAName},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		}
		util.MustCreate(ctx, k8sClient, secret)

		ginkgo.By("Creating the ConfigMap for the KubeConfig")
		kubeconfig, err := utiltesting.NewTestKubeConfigWrapper().
			Cluster("local", "https://kubernetes.default.svc", nil).
			CAFileCluster("local", "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt").
			User(customSAName, nil, nil).
			TokenFileAuthInfo(customSAName, customSAMountPath+"/token").
			Context("default", "local", customSAName).
			CurrentContext("default").
			Build()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: kueueNS},
			Data:       map[string]string{"config": string(kubeconfig)},
		}
		util.MustCreate(ctx, k8sClient, cm)

		ginkgo.By("Cloning all base permissions to our custom SA so the main controller can boot, explicitly holding back the auth delegation permission for our negative test.")
		cloneControllerRBAC(ctx)

		ginkgo.By("Creating the auth-reader binding so the visibility server can start up.")
		authReaderBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "visibility-test-auth-reader",
				Namespace: kubeSystemNamespace,
				Labels:    map[string]string{testLabelKey: testLabelValue},
			},
			RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "extension-apiserver-authentication-reader"},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNS}},
		}
		util.MustCreate(ctx, k8sClient, authReaderBinding)

		ginkgo.By("Creating a ClusterQueue")
		cq := &kueue.ClusterQueue{
			ObjectMeta: metav1.ObjectMeta{Name: cqName},
		}
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})

	ginkgo.AfterEach(func() {
		ginkgo.By("Restoring the original deployment")
		latestDeployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kueueManagerName, Namespace: kueueNS}, latestDeployment)).To(gomega.Succeed())
		latestDeployment.Spec = originalDeployment.Spec
		gomega.Expect(k8sClient.Update(ctx, latestDeployment)).To(gomega.Succeed())
		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		ginkgo.By("Cleaning up created resources")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: kueueNS}}, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: customSecretName, Namespace: kueueNS}}, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: customSAName, Namespace: kueueNS}}, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &kueue.ClusterQueue{ObjectMeta: metav1.ObjectMeta{Name: cqName}}, true)

		ginkgo.By("Cleaning up our dynamically cloned roles")
		gomega.Expect(k8sClient.DeleteAllOf(ctx, &rbacv1.ClusterRoleBinding{}, client.MatchingLabels{testLabelKey: testLabelValue})).To(gomega.Succeed())
		gomega.Expect(k8sClient.DeleteAllOf(ctx, &rbacv1.RoleBinding{}, client.InNamespace(kueueNS), client.MatchingLabels{testLabelKey: testLabelValue})).To(gomega.Succeed())
		gomega.Expect(k8sClient.DeleteAllOf(ctx, &rbacv1.RoleBinding{}, client.InNamespace(kubeSystemNamespace), client.MatchingLabels{testLabelKey: testLabelValue})).To(gomega.Succeed())
	})

	ginkgo.It("Should use the RBAC identity from the provided kubeconfig", func() {
		patchedDeployment := originalDeployment.DeepCopy()

		ginkgo.By("Mounting the ConfigMap and the Custom SA Token Secret")
		patchedDeployment.Spec.Template.Spec.Volumes = append(patchedDeployment.Spec.Template.Spec.Volumes,
			corev1.Volume{
				Name:         kubeconfigVolName,
				VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMapName}}},
			},
			corev1.Volume{
				Name:         customSAVolName,
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: customSecretName}},
			},
		)

		for i, c := range patchedDeployment.Spec.Template.Spec.Containers {
			if c.Name == "manager" {
				container := &patchedDeployment.Spec.Template.Spec.Containers[i]
				container.VolumeMounts = append(c.VolumeMounts,
					corev1.VolumeMount{Name: kubeconfigVolName, MountPath: "/etc/kubeconfig", ReadOnly: true},
					corev1.VolumeMount{Name: customSAVolName, MountPath: customSAMountPath, ReadOnly: true},
				)
				container.Args = append(c.Args, "--kubeconfig=/etc/kubeconfig/config")
			}
		}

		gomega.Expect(k8sClient.Update(ctx, patchedDeployment)).To(gomega.Succeed())
		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		// NEGATIVE TEST: The custom SA lacks 'system:auth-delegator'. The visibility server
		// is forced to use it, so it cannot perform SubjectAccessReviews.
		ginkgo.By("Expecting API requests to fail due to lack of auth delegation permissions")
		gomega.Eventually(func(g gomega.Gomega) {
			visClient := util.CreateVisibilityClient("")
			_, err := visClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, cqName, metav1.GetOptions{})
			g.Expect(err).To(gomega.HaveOccurred())

			// TODO: Fix the internal server error when lacking SubjectAccessReview permissions
			// See: https://github.com/kubernetes-sigs/kueue/issues/9779
			isExpectedError := k8serrors.IsUnauthorized(err) ||
				k8serrors.IsForbidden(err) ||
				k8serrors.IsInternalError(err) ||
				k8serrors.IsServiceUnavailable(err)

			g.Expect(isExpectedError).To(gomega.BeTrue(), "Expected an auth or internal error, but got: %v", err)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// POSITIVE TEST: Grant the required permissions to our custom ServiceAccount
		ginkgo.By("Granting permissions to the custom SA and verifying requests succeed")
		authDelegatorBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "visibility-test-auth-delegator",
				Labels: map[string]string{testLabelKey: testLabelValue},
			},
			RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:auth-delegator"},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNS}},
		}
		util.MustCreate(ctx, k8sClient, authDelegatorBinding)

		gomega.Eventually(func(g gomega.Gomega) {
			visClient := util.CreateVisibilityClient("")
			pw, err := visClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, cqName, metav1.GetOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(pw).NotTo(gomega.BeNil())
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
			if sub.Kind == "ServiceAccount" && sub.Name == "kueue-controller-manager" && sub.Namespace == kueueNS {
				// Skip the roles that grant SubjectAccessReview permissions
				if crb.RoleRef.Name == "system:auth-delegator" || crb.RoleRef.Name == "kueue-metrics-auth-role" {
					continue
				}
				newCRB := &rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "vistest-copy-" + crb.Name,
						Labels: map[string]string{testLabelKey: testLabelValue},
					},
					RoleRef:  crb.RoleRef,
					Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNS}},
				}
				util.MustCreate(ctx, k8sClient, newCRB)
			}
		}
	}

	// Clone RoleBindings (mostly for leader election in the kueue-system namespace)
	var rbs rbacv1.RoleBindingList
	gomega.Expect(k8sClient.List(ctx, &rbs, client.InNamespace(kueueNS))).To(gomega.Succeed())
	for _, rb := range rbs.Items {
		for _, sub := range rb.Subjects {
			if sub.Kind == "ServiceAccount" && sub.Name == "kueue-controller-manager" && sub.Namespace == kueueNS {
				newRB := &rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vistest-copy-" + rb.Name,
						Namespace: kueueNS,
						Labels:    map[string]string{testLabelKey: testLabelValue},
					},
					RoleRef:  rb.RoleRef,
					Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNS}},
				}
				util.MustCreate(ctx, k8sClient, newRB)
			}
		}
	}
}
