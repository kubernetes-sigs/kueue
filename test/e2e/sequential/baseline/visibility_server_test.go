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

package sequentialbaseline

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	kubeSystemNamespace       = "kube-system"
	kueueManagerName          = "kueue-controller-manager"
	kueueVisibilityServerName = "kueue-visibility-server"
	configMapName             = "visibility-kubeconfig-test"
	customSAName              = "visibility-test-sa"
	customSecretName          = "visibility-test-sa-token"
	testLabelKey              = "visibility-test-rbac"
	testLabelValue            = "true"
	kubeconfigVolName         = "kubeconfig-vol"
	customSAVolName           = "custom-sa-vol"
	customSAMountPath         = "/etc/custom-sa"
	cqName                    = "test-kubeconfig-cq"
	customVisibilityPort      = 9444
	tokenReviewerRoleName     = "visibility-test-token-reviewer"
	tokenReviewerBindingName  = "visibility-test-token-reviewer"
)

var _ = ginkgo.Describe("Visibility Server", ginkgo.Label("feature:visibility"), func() {
	var originalDeployment appsv1.Deployment
	var originalService corev1.Service
	var cq *kueue.ClusterQueue

	ginkgo.BeforeEach(func() {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: kueueManagerName, Namespace: kueueNS}, &originalDeployment)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = k8sClient.Get(ctx, types.NamespacedName{Name: kueueVisibilityServerName, Namespace: kueueNS}, &originalService)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Creating a ClusterQueue")
		cq = &kueue.ClusterQueue{
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

		ginkgo.By("Restoring the original service")
		latestService := &corev1.Service{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kueueVisibilityServerName, Namespace: kueueNS}, latestService)).To(gomega.Succeed())
		latestService.Spec.Ports = originalService.Spec.Ports
		gomega.Expect(k8sClient.Update(ctx, latestService)).To(gomega.Succeed())

		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		ginkgo.By("Cleaning up cluster queue")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
	})

	ginkgo.It("Should use the RBAC identity from the provided kubeconfig", func() {
		ginkgo.By("Creating Custom ServiceAccount")
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: customSAName, Namespace: kueueNS}}
		util.MustCreate(ctx, k8sClient, sa)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, sa, true)
		})

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
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, secret, true)
		})

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
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cm, true)
		})

		ginkgo.By("Cloning all base permissions to our custom SA so the main controller can boot, explicitly holding back the auth delegation permission for our negative test.")
		cloneControllerRBAC(ctx)
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Cleaning up our dynamically cloned roles")
			gomega.Expect(k8sClient.DeleteAllOf(ctx, &rbacv1.ClusterRole{}, client.MatchingLabels{testLabelKey: testLabelValue})).To(gomega.Succeed())
			gomega.Expect(k8sClient.DeleteAllOf(ctx, &rbacv1.ClusterRoleBinding{}, client.MatchingLabels{testLabelKey: testLabelValue})).To(gomega.Succeed())
			gomega.Expect(k8sClient.DeleteAllOf(ctx, &rbacv1.RoleBinding{}, client.InNamespace(kueueNS), client.MatchingLabels{testLabelKey: testLabelValue})).To(gomega.Succeed())
			gomega.Expect(k8sClient.DeleteAllOf(ctx, &rbacv1.RoleBinding{}, client.InNamespace(kubeSystemNamespace), client.MatchingLabels{testLabelKey: testLabelValue})).To(gomega.Succeed())
		})

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

		ginkgo.By("Granting token review permissions so delegated authentication still works during the negative authorization check.")
		tokenReviewerRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   tokenReviewerRoleName,
				Labels: map[string]string{testLabelKey: testLabelValue},
			},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"authentication.k8s.io"},
				Resources: []string{"tokenreviews"},
				Verbs:     []string{"create"},
			}},
		}
		util.MustCreate(ctx, k8sClient, tokenReviewerRole)

		tokenReviewerBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:   tokenReviewerBindingName,
				Labels: map[string]string{testLabelKey: testLabelValue},
			},
			RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: tokenReviewerRoleName},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: customSAName, Namespace: kueueNS}},
		}
		util.MustCreate(ctx, k8sClient, tokenReviewerBinding)

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

		// NEGATIVE TEST: The request is authenticated via TokenReview, but the visibility server
		// itself cannot complete delegated authorization because its identity lacks SubjectAccessReview permission.
		ginkgo.By("Expecting API requests to fail with internal error due to delegated authorization misconfiguration")
		gomega.Eventually(func(g gomega.Gomega) {
			visClient := util.CreateVisibilityClient("")
			_, err := visClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, cqName, metav1.GetOptions{})
			g.Expect(err).To(gomega.HaveOccurred())

			g.Expect(k8serrors.IsInternalError(err)).To(gomega.BeTrue(), "Expected an internal error, but got: %v", err)
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

	ginkgo.It("Should use the custom port from the --visibility-server-port flag", func() {
		ginkgo.By("Updating the visibility-server service's targetPort")
		patchedService := originalService.DeepCopy()
		for i, p := range patchedService.Spec.Ports {
			if p.Name == "https" {
				patchedService.Spec.Ports[i].TargetPort = intstr.FromInt32(customVisibilityPort)
			}
		}
		gomega.Expect(k8sClient.Update(ctx, patchedService)).To(gomega.Succeed())

		ginkgo.By("Updating the visibility-server deployment's port")
		patchedDeployment := originalDeployment.DeepCopy()
		for i, c := range patchedDeployment.Spec.Template.Spec.Containers {
			if c.Name == "manager" {
				container := &patchedDeployment.Spec.Template.Spec.Containers[i]
				container.Args = append(c.Args, fmt.Sprintf("--visibility-server-port=%d", customVisibilityPort))
			}
		}
		gomega.Expect(k8sClient.Update(ctx, patchedDeployment)).To(gomega.Succeed())
		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		ginkgo.By("Verifying requests succeed on the custom port")
		gomega.Eventually(func(g gomega.Gomega) {
			visClient := util.CreateVisibilityClient("")
			pw, err := visClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, cqName, metav1.GetOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(pw).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should use the custom port from the visibilityServer configuration API", func() {
		ginkgo.By("Updating the visibilityServer configuration and restarting Kueue")
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.VisibilityServer = &configapi.VisibilityServerConfiguration{
				BindPort: ptr.To[int32](customVisibilityPort),
			}
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Restoring original configuration")
			util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
		})

		ginkgo.By("Updating the visibility-server service's targetPort")
		patchedService := originalService.DeepCopy()
		for i, p := range patchedService.Spec.Ports {
			if p.Name == "https" {
				patchedService.Spec.Ports[i].TargetPort = intstr.FromInt32(customVisibilityPort)
			}
		}
		gomega.Expect(k8sClient.Update(ctx, patchedService)).To(gomega.Succeed())

		ginkgo.By("Verifying requests succeed on the custom port")
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
