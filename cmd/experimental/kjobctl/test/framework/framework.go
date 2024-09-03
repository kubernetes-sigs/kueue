/*
Copyright 2024 The Kubernetes Authors.

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

package framework

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

type Framework struct {
	CRDPath     string
	DepCRDPaths []string
	testEnv     *envtest.Environment
	cancel      context.CancelFunc
}

func (f *Framework) Init() *rest.Config {
	ginkgo.By("bootstrapping test environment")

	f.testEnv = &envtest.Environment{
		CRDDirectoryPaths:     append(f.DepCRDPaths, f.CRDPath),
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := f.testEnv.Start()

	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	return cfg
}

func (f *Framework) SetupClient(cfg *rest.Config) (context.Context, client.Client) {
	err := v1alpha1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = rayv1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, k8sClient).NotTo(gomega.BeNil())

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel

	return ctx, k8sClient
}

func (f *Framework) Teardown() {
	ginkgo.By("tearing down the test environment")
	if f.cancel != nil {
		f.cancel()
	}
	err := f.testEnv.Stop()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
}
