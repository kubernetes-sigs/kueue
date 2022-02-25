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

package framework

import (
	"context"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueuev1alpha1 "sigs.k8s.io/kueue/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

type ManagerSetup func(manager.Manager)

func BeforeSuite(ctx context.Context, crdPath string, mgrSetup ManagerSetup) (*rest.Config, client.Client, *envtest.Environment) {
	ctrl.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true)))

	ginkgo.By("bootstrapping test environment")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	err = kueuev1alpha1.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, k8sClient).NotTo(gomega.BeNil())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0", // disable metrics to avoid conflicts between packages.
	})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "failed to create manager")

	mgrSetup(mgr)

	go func() {
		defer ginkgo.GinkgoRecover()
		err := mgr.Start(ctx)
		gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "failed to run manager")
	}()

	return cfg, k8sClient, testEnv
}

func AfterSuite(testEnv *envtest.Environment) {
	ginkgo.By("tearing down the test environment")
	err := testEnv.Stop()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
}
