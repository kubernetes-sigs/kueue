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

package provisioning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/yaml"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Provisioning with manager RBAC and OwnerReferencesPermissionEnforcement", ginkgo.Label("controller:provisioning", "area:admissionchecks", "feature:provisioning"), func() {
	var (
		ns            *corev1.Namespace
		rf            *kueue.ResourceFlavor
		prc           *kueue.ProvisioningRequestConfig
		ac            *kueue.AdmissionCheck
		cq            *kueue.ClusterQueue
		lq            *kueue.LocalQueue
		managerClient client.Client
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "provisioning-rbac-")

		roleBytes, err := os.ReadFile(filepath.Join(behavioral.ProjectBaseDir, "config", "components", "rbac", "role.yaml"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		role := &rbacv1.ClusterRole{}
		gomega.Expect(yaml.Unmarshal(roleBytes, role)).To(gomega.Succeed())
		role.Name = ns.Name + "-manager-role"
		behavioral.MustCreate(ctx, k8sClient, role)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, role, true)
		})

		sa := &corev1.ServiceAccount{
			Name:      "kueue-manager",
			Namespace: ns.Name,
		}
		behavioral.MustCreate(ctx, k8sClient, sa)

		binding := &rbacv1.ClusterRoleBinding{
			Name: ns.Name + "-manager-rolebinding",
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     role.Name,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      sa.Name,
				Namespace: sa.Namespace,
			}},
		}
		behavioral.MustCreate(ctx, k8sClient, binding)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, binding, true)
		})

		saUser := fmt.Sprintf("system:serviceaccount:%s:%s", sa.Namespace, sa.Name)
		clientset, err := kubernetes.NewForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func(g gomega.Gomega) {
			review, err := clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
				Spec: authorizationv1.SubjectAccessReviewSpec{
					User: saUser,
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Namespace: ns.Name,
						Verb:      "create",
						Group:     autoscaling.GroupName,
						Resource:  "provisioningrequests",
					},
				},
			}, metav1.CreateOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(review.Status.Allowed).To(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		managerCfg := rest.CopyConfig(cfg)
		managerCfg.Impersonate = rest.ImpersonationConfig{
			UserName: saUser,
		}
		setup := managerSetup()
		fwk.StartManagerForSpec(ctx, managerCfg, func(ctx context.Context, mgr manager.Manager) {
			managerClient = mgr.GetClient()
			setup(ctx, mgr)
		})

		rf = utiltestingapi.MakeResourceFlavor("rbac-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		prc = utiltestingapi.MakeProvisioningRequestConfig("rbac-prov-config").
			ProvisioningClass("provisioning-class").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, prc)

		ac = utiltestingapi.MakeAdmissionCheck("rbac-ac-prov").
			ControllerName(kueue.ProvisioningRequestControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ac)

		cq = utiltestingapi.MakeClusterQueue("rbac-cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("rbac-queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
	})

	ginkgo.It("Should transfer PodTemplate ownership to ProvisioningRequest under manager RBAC", func() {
		wl := utiltestingapi.MakeWorkload("wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(
				*utiltestingapi.MakePodSet("ps1", 2).
					Request(corev1.ResourceCPU, "1").
					Image("image").
					Obj(),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		wlKey := client.ObjectKeyFromObject(wl)

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).
			PodSets(
				utiltestingapi.MakePodSetAssignment("ps1").
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "2").
					Count(2).
					Obj(),
			).
			Obj()

		ginkgo.By("Reserving quota for the workload", func() {
			integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
		})

		provReqKey := types.NamespacedName{
			Namespace: ns.Name,
			Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
		}
		createdRequest := &autoscaling.ProvisioningRequest{}
		ginkgo.By("Waiting for the ProvisioningRequest to be created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, provReqKey, createdRequest)).To(gomega.Succeed())
				g.Expect(createdRequest.Spec.PodSets).To(gomega.HaveLen(1))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		templateKey := types.NamespacedName{
			Namespace: ns.Name,
			Name:      createdRequest.Spec.PodSets[0].PodTemplateRef.Name,
		}
		ginkgo.By("Accepting the ProvisioningRequest once the manager cache sees its PodTemplate", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerClient.Get(ctx, templateKey, &corev1.PodTemplate{})).To(gomega.Succeed())

				req := &autoscaling.ProvisioningRequest{}
				g.Expect(k8sClient.Get(ctx, provReqKey, req)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
					Type:   autoscaling.Accepted,
					Status: metav1.ConditionTrue,
					Reason: autoscaling.Accepted,
				})
				g.Expect(k8sClient.Status().Update(ctx, req)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("Checking that PodTemplate controller ownership is transferred to the ProvisioningRequest", func() {
			createdTemplate := &corev1.PodTemplate{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, templateKey, createdTemplate)).To(gomega.Succeed())
				g.Expect(metav1.IsControlledBy(createdTemplate, createdRequest)).To(
					gomega.BeTrue(),
					"expected PodTemplate to be controlled by ProvisioningRequest, got ownerReferences: %+v",
					createdTemplate.OwnerReferences,
				)
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
