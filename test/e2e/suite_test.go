/*
Copyright 2022 The Kubernetes Authors.

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
	"context"
	"fmt"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	visibilityv1alpha1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1alpha1"
	kueuetest "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
	//+kubebuilder:scaffold:imports
)

var (
	k8sClient                    client.Client
	ctx                          context.Context
	visibilityClient             visibilityv1alpha1.VisibilityV1alpha1Interface
	impersonatedVisibilityClient visibilityv1alpha1.VisibilityV1alpha1Interface
)

const (
	Timeout  = time.Minute
	Interval = time.Millisecond * 250
)

func TestAPIs(t *testing.T) {
	suiteName := "End To End Suite"
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = fmt.Sprintf("%s: %s", suiteName, ver)
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t,
		suiteName,
	)
}

func CreateClientUsingCluster() client.Client {
	cfg := config.GetConfigOrDie()
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	kueueClient, err := kueueclientset.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}
	visibilityClient = kueueClient.VisibilityV1alpha1()

	err = kueue.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kftraining.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = visibility.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	// +kubebuilder:scaffold:scheme
	client, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	cfg.Impersonate.UserName = "system:serviceaccount:kueue-system:default"
	impersonatedKueueClient, err := kueueclientset.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}
	impersonatedVisibilityClient = impersonatedKueueClient.VisibilityV1alpha1()

	return client
}

func KueueReadyForTesting(client client.Client) {
	// To verify that webhooks are ready, let's create a simple resourceflavor
	resourceKueue := kueuetest.MakeResourceFlavor("default").Obj()
	gomega.Eventually(func() error {
		return client.Create(context.Background(), resourceKueue)
	}, Timeout, Interval).Should(gomega.Succeed())
	util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceKueue, true)
}

var _ = ginkgo.BeforeSuite(func() {
	k8sClient = CreateClientUsingCluster()
	ctx = context.Background()
	KueueReadyForTesting(k8sClient)
})
