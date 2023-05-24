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
	"crypto/tls"
	"fmt"
	"net"
	"time"

	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayjobapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type ManagerSetup func(manager.Manager, context.Context)

type Framework struct {
	CRDPath      string
	DepCRDPaths  []string
	WebhookPath  string
	ManagerSetup ManagerSetup
	testEnv      *envtest.Environment
	cancel       context.CancelFunc
}

func (f *Framework) Setup() (context.Context, *rest.Config, client.Client) {
	opts := func(o *zap.Options) {
		o.TimeEncoder = zapcore.RFC3339NanoTimeEncoder
		o.ZapOpts = []zaplog.Option{zaplog.AddCaller()}
	}
	ctrl.SetLogger(zap.New(
		zap.WriteTo(ginkgo.GinkgoWriter),
		zap.UseDevMode(true),
		zap.Level(zapcore.Level(-3)),
		opts),
	)

	ginkgo.By("bootstrapping test environment")
	f.testEnv = &envtest.Environment{
		CRDDirectoryPaths:     append(f.DepCRDPaths, f.CRDPath),
		ErrorIfCRDPathMissing: true,
	}
	webhookEnabled := len(f.WebhookPath) > 0
	if webhookEnabled {
		f.testEnv.WebhookInstallOptions.Paths = []string{f.WebhookPath}
	}

	cfg, err := f.testEnv.Start()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())

	err = kueue.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kubeflow.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = rayjobapi.AddToScheme(scheme.Scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	// +kubebuilder:scaffold:scheme

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, k8sClient).NotTo(gomega.BeNil())

	webhookInstallOptions := &f.testEnv.WebhookInstallOptions
	mgrOpts := manager.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0", // disable metrics to avoid conflicts between packages.
		Host:               webhookInstallOptions.LocalServingHost,
		Port:               webhookInstallOptions.LocalServingPort,
		CertDir:            webhookInstallOptions.LocalServingCertDir,
	}
	mgr, err := ctrl.NewManager(cfg, mgrOpts)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "failed to create manager")

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.ManagerSetup(mgr, ctx)

	go func() {
		defer ginkgo.GinkgoRecover()
		err := mgr.Start(ctx)
		gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "failed to run manager")
	}()

	if webhookEnabled {
		// wait for the webhook server to get ready
		dialer := &net.Dialer{Timeout: time.Second}
		addrPort := fmt.Sprintf("%s:%d", webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
		gomega.Eventually(func() error {
			conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
			if err != nil {
				return err
			}
			conn.Close()
			return nil
		}).Should(gomega.Succeed())
	}

	return ctx, cfg, k8sClient
}

func (f *Framework) Teardown() {
	ginkgo.By("tearing down the test environment")
	f.cancel()
	err := f.testEnv.Stop()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
}
