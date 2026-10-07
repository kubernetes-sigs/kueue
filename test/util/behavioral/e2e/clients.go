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

package e2e

import (
	"fmt"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta2"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	visibilityv1beta2 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1beta2"
)

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

	err = autoscaling.AddToScheme(scheme.Scheme)
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

	err = sparkv1beta2.AddToScheme(scheme.Scheme)
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

func GetAuthInfoFromKubeConfig(kubeConfig []byte) *clientcmdapi.AuthInfo {
	ginkgo.GinkgoHelper()
	cfg, err := clientcmd.Load(kubeConfig)
	gomega.Expect(err).To(gomega.Succeed())
	return cfg.AuthInfos[cfg.Contexts[cfg.CurrentContext].AuthInfo]
}
