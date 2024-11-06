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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/apimachinery/pkg/runtime"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/test/util"
)

type ManagerSetup func(context.Context, manager.Manager)

type Framework struct {
	CRDPath               string
	DepCRDPaths           []string
	WebhookPath           string
	APIServerFeatureGates []string
	testEnv               *envtest.Environment
	cancel                context.CancelFunc
	scheme                *runtime.Scheme

	managerCancel context.CancelFunc
	managerDone   <-chan struct{}
}

var setupLogger = sync.OnceFunc(func() {
	ctrl.SetLogger(util.NewTestingLogger(ginkgo.GinkgoWriter, -3))
})

func (f *Framework) Init() *rest.Config {
	setupLogger()

	var cfg *rest.Config
	ginkgo.By("bootstrapping test environment", func() {
		f.testEnv = &envtest.Environment{
			CRDDirectoryPaths:     append(f.DepCRDPaths, f.CRDPath),
			ErrorIfCRDPathMissing: true,
		}
		if len(f.WebhookPath) > 0 {
			f.testEnv.WebhookInstallOptions.Paths = []string{f.WebhookPath}
		}

		if len(f.APIServerFeatureGates) > 0 {
			f.testEnv.ControlPlane.GetAPIServer().Configure().Append("feature-gates", strings.Join(f.APIServerFeatureGates, ","))
		}

		if level, err := strconv.Atoi(os.Getenv("API_LOG_LEVEL")); err == nil && level > 0 {
			f.testEnv.ControlPlane.GetAPIServer().Configure().Append("v", strconv.Itoa(level))
			f.testEnv.ControlPlane.GetAPIServer().Out = ginkgo.GinkgoWriter
			f.testEnv.ControlPlane.GetAPIServer().Err = ginkgo.GinkgoWriter
		}

		var err error
		cfg, err = f.testEnv.Start()
		gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
		gomega.ExpectWithOffset(1, cfg).NotTo(gomega.BeNil())
	})
	f.scheme = runtime.NewScheme()
	gomega.ExpectWithOffset(1, clientgoscheme.AddToScheme(f.scheme)).NotTo(gomega.HaveOccurred())
	return cfg
}

func (f *Framework) SetupClient(cfg *rest.Config) (context.Context, client.Client) {
	err := config.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kueue.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kueuealpha.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kfmpi.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = rayv1.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = jobsetapi.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = kftraining.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	err = autoscaling.AddToScheme(f.scheme)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())

	k8sClient, err := client.New(cfg, client.Options{Scheme: f.scheme})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, k8sClient).NotTo(gomega.BeNil())

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel

	return ctx, k8sClient
}

func (f *Framework) StartManager(ctx context.Context, cfg *rest.Config, managerSetup ManagerSetup) {
	ginkgo.By("starting the manager", func() {
		webhookInstallOptions := &f.testEnv.WebhookInstallOptions
		mgrOpts := manager.Options{
			Scheme: f.scheme,
			Metrics: metricsserver.Options{
				BindAddress: "0", // disable metrics to avoid conflicts between packages.
			},
			WebhookServer: webhook.NewServer(
				webhook.Options{
					Host:    webhookInstallOptions.LocalServingHost,
					Port:    webhookInstallOptions.LocalServingPort,
					CertDir: webhookInstallOptions.LocalServingCertDir,
				}),
			Controller: crconfig.Controller{
				SkipNameValidation: ptr.To(true),
			},
		}
		mgr, err := ctrl.NewManager(cfg, mgrOpts)
		gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "failed to create manager")

		managerCtx, managerCancel := context.WithCancel(ctx)
		managerSetup(managerCtx, mgr)

		done := make(chan struct{})

		f.managerCancel = managerCancel
		f.managerDone = done

		go func() {
			defer close(done)
			defer ginkgo.GinkgoRecover()
			err := mgr.Start(managerCtx)
			gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "failed to run manager")
		}()

		if len(f.WebhookPath) > 0 {
			// wait for the webhook server to get ready
			dialer := &net.Dialer{Timeout: time.Second}
			addrPort := fmt.Sprintf("%s:%d", webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
			gomega.Eventually(func(g gomega.Gomega) {
				conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
				g.Expect(err).NotTo(gomega.HaveOccurred())
				conn.Close()
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		}
	})
}

func (f *Framework) StopManager(ctx context.Context) {
	ginkgo.By("stopping the manager", func() {
		if f.managerCancel == nil {
			return
		}

		f.managerCancel()
		select {
		case <-f.managerDone:
			ginkgo.GinkgoLogr.Info("manager stopped")
		case <-ctx.Done():
			ginkgo.GinkgoLogr.Info("manager stop canceled")
		}
	})
}

func (f *Framework) Teardown() {
	ginkgo.By("tearing down the test environment")
	if f.cancel != nil {
		f.cancel()
	}
	err := f.testEnv.Stop()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
}

var (
	// SlowSpec label used to decorate test specs that take a long time to be run and
	// should be skipped in PR builds
	//
	// Initial list base on:
	//  curl https://storage.googleapis.com/kubernetes-jenkins/pr-logs/pull/kubernetes-sigs_kueue/3054/pull-kueue-test-integration-main/1836045641336229888/artifacts/integration-top.yaml \
	// | yq '.[] | select(.name != "") | .f.duration = .duration | .f.name = .name | .f.suite=.suite | .f | [] + .' | yq '.[0:30]' -oc
	//
	// taking the item which run for more then 5 sec
	SlowSpec = ginkgo.Label("slow")

	// RedundantSpec label used to decorate test specs that largely cover generic code covered by other specs also. (eg. Kubeflow jobs)
	RedundantSpec = ginkgo.Label("Redundant")
)
