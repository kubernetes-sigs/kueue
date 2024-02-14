/*
Copyright 2023 The Kubernetes Authors.

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
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/test/integration/framework"
	// +kubebuilder:scaffold:imports
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
)

func TestProvisioning(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Provisioning admission check suite",
	)
}

func managerSetup(mgr manager.Manager, ctx context.Context) {
	err := autoscaling.AddToScheme(mgr.GetScheme())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	reconciler, err := provisioning.NewController(mgr.GetClient(), mgr.GetEventRecorderFor("kueue-provisioning-request-controller"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = reconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		CRDPath:     filepath.Join("..", "..", "..", "..", "..", "config", "components", "crd", "bases"),
		DepCRDPaths: []string{filepath.Join("..", "..", "..", "..", "..", "dep-crds", "cluster-autoscaler")},
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.RunManager(cfg, managerSetup)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})
