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

package replicaset

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/deployment/replicaset"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"ReplicaSet Controller Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		reconciler, err := replicaset.NewReconciler(ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reconciler.SetupWithManager(mgr)).NotTo(gomega.HaveOccurred())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(indexer.Setup(ctx, mgr.GetFieldIndexer())).NotTo(gomega.HaveOccurred())
		gomega.Expect(replicaset.SetupIndexes(ctx, mgr.GetFieldIndexer())).NotTo(gomega.HaveOccurred())
		gomega.Expect(replicaset.SetupWebhook(mgr, opts...)).NotTo(gomega.HaveOccurred())
		failedWebhook, err := webhooks.Setup(mgr)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)
	}
}
