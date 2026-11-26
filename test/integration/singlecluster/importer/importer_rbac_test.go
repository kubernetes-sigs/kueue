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

package importer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	importercache "sigs.k8s.io/kueue/cmd/importer/cache"
	importermapping "sigs.k8s.io/kueue/cmd/importer/mapping"
	importerpod "sigs.k8s.io/kueue/cmd/importer/pod"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

func loadImporterClusterRole() *rbacv1.ClusterRole {
	ginkgo.GinkgoHelper()

	manifestBytes, err := os.ReadFile(filepath.Join(behavioral.ProjectBaseDir, "cmd", "importer", "run-in-cluster", "deps.yaml"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(manifestBytes), 4096)
	for {
		var role rbacv1.ClusterRole
		err := decoder.Decode(&role)
		if errors.Is(err, io.EOF) {
			break
		}
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if role.Kind == "ClusterRole" && role.Name == "kueue-importer" {
			return &role
		}
	}
	ginkgo.Fail("ClusterRole kueue-importer not found in cmd/importer/run-in-cluster/deps.yaml")
	return nil
}

var _ = ginkgo.Describe("Importer with RBAC and OwnerReferencesPermissionEnforcement", func() {
	var (
		ns             *corev1.Namespace
		rf             *kueue.ResourceFlavor
		cq             *kueue.ClusterQueue
		lq             *kueue.LocalQueue
		importerClient client.Client
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "importer-rbac-")

		role := loadImporterClusterRole()
		role.Name = ns.Name + "-kueue-importer"
		behavioral.MustCreate(ctx, k8sClient, role)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, role, true)
		})

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kueue-importer",
				Namespace: ns.Name,
			},
		}
		behavioral.MustCreate(ctx, k8sClient, sa)

		binding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: ns.Name + "-kueue-importer",
			},
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
						Group:     kueue.SchemeGroupVersion.Group,
						Resource:  "workloads",
					},
				},
			}, metav1.CreateOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(review.Status.Allowed).To(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		importerCfg := rest.CopyConfig(cfg)
		importerCfg.Impersonate = rest.ImpersonationConfig{
			UserName: saUser,
		}
		importerClient, err = client.New(importerCfg, client.Options{Scheme: k8sClient.Scheme()})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		rf = utiltestingapi.MakeResourceFlavor("importer-rbac-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("importer-rbac-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("importer-rbac-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.It("Should import a pod using the in-cluster importer ClusterRole", func() {
		p := utiltestingpod.MakePod("pod1", ns.Name).
			Label("src.lbl", "src-val").
			Request(corev1.ResourceCPU, "2").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, p)

		mapping, err := importercache.Load(ctx, importerClient, []string{ns.Name}, importermapping.RulesForLabel("src.lbl", map[string]string{"src-val": lq.Name}), nil, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(importerpod.Check(ctx, importerClient, mapping, 1)).To(gomega.Succeed())
		gomega.Expect(importerpod.Import(ctx, importerClient, mapping, 1)).To(gomega.Succeed())

		wlKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(p.Name, p.UID), Namespace: ns.Name}
		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		gomega.Expect(metav1.IsControlledBy(wl, p)).To(gomega.BeTrue())
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
	})
})
