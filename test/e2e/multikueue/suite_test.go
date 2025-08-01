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

package mke2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/test/util"
)

var (
	managerK8SVersion  *versionutil.Version
	managerClusterName string
	worker1ClusterName string
	worker2ClusterName string
	kueueNS            = util.GetKueueNamespace()

	k8sManagerClient client.Client
	k8sWorker1Client client.Client
	k8sWorker2Client client.Client
	ctx              context.Context

	managerCfg *rest.Config
	worker1Cfg *rest.Config
	worker2Cfg *rest.Config

	managerRestClient *rest.RESTClient
	worker1RestClient *rest.RESTClient
	worker2RestClient *rest.RESTClient
)

func policyRule(group, resource string, verbs ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{
		APIGroups: []string{group},
		Resources: []string{resource},
		Verbs:     verbs,
	}
}

// kubeconfigForMultiKueueSA - returns the content of a kubeconfig that could be used by a multikueue manager to connect to a worker.
//
// In the target cluster it will create:
// - one ClusterRole <prefix>-cr allowing all the multikueue related operations.
// - one ServiceAccount <prefix>-sa, bound to <prefix>-cr, from which an authentication token is generated.
//
// The resulting kubeconfig is composed based on the provided restConfig with two notable changes:
// - the authentication is done with a token generated for <prefix>-sa.
// - the server URL is set to https://<clusterName>-control-plane:6443.
func kubeconfigForMultiKueueSA(ctx context.Context, c client.Client, restConfig *rest.Config, ns string, prefix string, clusterName string) ([]byte, error) {
	roleName := prefix + "-role"
	resourceVerbs := []string{"create", "delete", "get", "list", "watch"}
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		Rules: []rbacv1.PolicyRule{
			policyRule(batchv1.SchemeGroupVersion.Group, "jobs", resourceVerbs...),
			policyRule(batchv1.SchemeGroupVersion.Group, "jobs/status", "get"),
			policyRule(jobset.SchemeGroupVersion.Group, "jobsets", resourceVerbs...),
			policyRule(jobset.SchemeGroupVersion.Group, "jobsets/status", "get"),
			policyRule(kueue.SchemeGroupVersion.Group, "workloads", resourceVerbs...),
			policyRule(kueue.SchemeGroupVersion.Group, "workloads/status", "get", "patch", "update"),
			policyRule(kftraining.SchemeGroupVersion.Group, "tfjobs", resourceVerbs...),
			policyRule(kftraining.SchemeGroupVersion.Group, "tfjobs/status", "get"),
			policyRule(kftraining.SchemeGroupVersion.Group, "paddlejobs", resourceVerbs...),
			policyRule(kftraining.SchemeGroupVersion.Group, "paddlejobs/status", "get"),
			policyRule(kftraining.SchemeGroupVersion.Group, "pytorchjobs", resourceVerbs...),
			policyRule(kftraining.SchemeGroupVersion.Group, "pytorchjobs/status", "get"),
			policyRule(kftraining.SchemeGroupVersion.Group, "xgboostjobs", resourceVerbs...),
			policyRule(kftraining.SchemeGroupVersion.Group, "xgboostjobs/status", "get"),
			policyRule(kftraining.SchemeGroupVersion.Group, "jaxjobs", resourceVerbs...),
			policyRule(kftraining.SchemeGroupVersion.Group, "jaxjobs/status", "get"),
			policyRule(awv1beta2.GroupVersion.Group, "appwrappers", resourceVerbs...),
			policyRule(awv1beta2.GroupVersion.Group, "appwrappers/status", "get"),
			policyRule(kfmpi.SchemeGroupVersion.Group, "mpijobs", resourceVerbs...),
			policyRule(kfmpi.SchemeGroupVersion.Group, "mpijobs/status", "get"),
			policyRule(rayv1.SchemeGroupVersion.Group, "rayjobs", resourceVerbs...),
			policyRule(rayv1.SchemeGroupVersion.Group, "rayjobs/status", "get"),
			policyRule(corev1.SchemeGroupVersion.Group, "pods", resourceVerbs...),
			policyRule(corev1.SchemeGroupVersion.Group, "pods/status", "get"),
			policyRule(rayv1.SchemeGroupVersion.Group, "rayclusters", resourceVerbs...),
			policyRule(rayv1.SchemeGroupVersion.Group, "rayclusters/status", "get"),
		},
	}
	err := c.Create(ctx, cr)
	if err != nil {
		return nil, err
	}

	saName := prefix + "-sa"
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      saName,
		},
	}
	err = c.Create(ctx, sa)
	if err != nil {
		return nil, err
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: prefix + "-crb"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: ns,
			},
		},
	}
	err = c.Create(ctx, crb)
	if err != nil {
		return nil, err
	}

	// get the token
	token := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			// The 1h expiration duration is the default value.
			// It is explicitly mentioned for documentation purposes.
			ExpirationSeconds: ptr.To[int64](3600),
		},
	}
	err = c.SubResource("token").Create(ctx, sa, token)
	if err != nil {
		return nil, err
	}

	cfg := clientcmdapi.Config{
		Kind:       "config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"default-cluster": {
				Server:                   "https://" + clusterName + "-control-plane:6443",
				CertificateAuthorityData: restConfig.CAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default-user": {
				Token: token.Status.Token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default-context": {
				Cluster:  "default-cluster",
				AuthInfo: "default-user",
			},
		},
		CurrentContext: "default-context",
	}
	return clientcmd.Write(cfg)
}

func cleanKubeconfigForMultiKueueSA(ctx context.Context, c client.Client, ns string, prefix string) error {
	roleName := prefix + "-role"

	err := c.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	err = c.Delete(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: prefix + "-sa"}})
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	err = c.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-crb"}})
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	return nil
}

func makeMultiKueueSecret(ctx context.Context, c client.Client, namespace string, name string, kubeconfig []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfig,
		},
	}
	return c.Create(ctx, secret)
}

func cleanMultiKueueSecret(ctx context.Context, c client.Client, namespace string, name string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
	return client.IgnoreNotFound(c.Delete(ctx, secret))
}

func TestAPIs(t *testing.T) {
	suiteName := "End To End MultiKueue Suite"
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = fmt.Sprintf("%s: %s", suiteName, ver)
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t,
		suiteName,
	)
}

var _ = ginkgo.BeforeSuite(func() {
	util.SetupLogger()

	managerClusterName = os.Getenv("MANAGER_KIND_CLUSTER_NAME")
	gomega.Expect(managerClusterName).NotTo(gomega.BeEmpty(), "MANAGER_KIND_CLUSTER_NAME should not be empty")

	worker1ClusterName = os.Getenv("WORKER1_KIND_CLUSTER_NAME")
	gomega.Expect(worker1ClusterName).NotTo(gomega.BeEmpty(), "WORKER1_KIND_CLUSTER_NAME should not be empty")

	worker2ClusterName = os.Getenv("WORKER2_KIND_CLUSTER_NAME")
	gomega.Expect(worker2ClusterName).NotTo(gomega.BeEmpty(), "WORKER2_KIND_CLUSTER_NAME should not be empty")

	var err error
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sManagerClient, managerCfg, err = util.CreateClientUsingCluster("kind-" + managerClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sWorker1Client, worker1Cfg, err = util.CreateClientUsingCluster("kind-" + worker1ClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sWorker2Client, worker2Cfg, err = util.CreateClientUsingCluster("kind-" + worker2ClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	managerRestClient = util.CreateRestClient(managerCfg)
	worker1RestClient = util.CreateRestClient(worker1Cfg)
	worker2RestClient = util.CreateRestClient(worker2Cfg)

	ctx = ginkgo.GinkgoT().Context()

	worker1Kconfig, err := kubeconfigForMultiKueueSA(ctx, k8sWorker1Client, worker1Cfg, kueueNS, "mksa", worker1ClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(makeMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue1", worker1Kconfig)).To(gomega.Succeed())

	worker2Kconfig, err := kubeconfigForMultiKueueSA(ctx, k8sWorker2Client, worker2Cfg, kueueNS, "mksa", worker2ClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(makeMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue2", worker2Kconfig)).To(gomega.Succeed())

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sManagerClient)
	util.WaitForKueueAvailability(ctx, k8sWorker1Client)
	util.WaitForKueueAvailability(ctx, k8sWorker2Client)

	util.WaitForJobSetAvailability(ctx, k8sManagerClient)
	util.WaitForJobSetAvailability(ctx, k8sWorker1Client)
	util.WaitForJobSetAvailability(ctx, k8sWorker2Client)

	util.WaitForKubeFlowTrainingOperatorAvailability(ctx, k8sManagerClient)
	util.WaitForKubeFlowTrainingOperatorAvailability(ctx, k8sWorker1Client)
	util.WaitForKubeFlowTrainingOperatorAvailability(ctx, k8sWorker2Client)

	util.WaitForKubeFlowMPIOperatorAvailability(ctx, k8sWorker1Client)
	util.WaitForKubeFlowMPIOperatorAvailability(ctx, k8sWorker2Client)

	util.WaitForAppWrapperAvailability(ctx, k8sManagerClient)
	util.WaitForAppWrapperAvailability(ctx, k8sWorker1Client)
	util.WaitForAppWrapperAvailability(ctx, k8sWorker2Client)

	util.WaitForKubeRayOperatorAvailability(ctx, k8sManagerClient)
	util.WaitForKubeRayOperatorAvailability(ctx, k8sWorker1Client)
	util.WaitForKubeRayOperatorAvailability(ctx, k8sWorker2Client)

	ginkgo.GinkgoLogr.Info(
		"Kueue and all required operators are available in all the clusters",
		"waitingTime", time.Since(waitForAvailableStart),
	)
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(managerCfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	managerK8SVersion, err = kubeversion.FetchServerVersion(discoveryClient)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

var _ = ginkgo.AfterSuite(func() {
	gomega.Expect(cleanKubeconfigForMultiKueueSA(ctx, k8sWorker1Client, kueueNS, "mksa")).To(gomega.Succeed())
	gomega.Expect(cleanKubeconfigForMultiKueueSA(ctx, k8sWorker2Client, kueueNS, "mksa")).To(gomega.Succeed())

	gomega.Expect(cleanMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue1")).To(gomega.Succeed())
	gomega.Expect(cleanMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue2")).To(gomega.Succeed())
})
