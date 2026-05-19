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

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	boostconstants "sigs.k8s.io/kueue/cmd/experimental/kueue-priority-booster/pkg/constants"
	boostctrl "sigs.k8s.io/kueue/cmd/experimental/kueue-priority-booster/pkg/controller"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
)

// integrationNegativeBoost matches default production magnitude; asserted in specs.
const integrationNegativeBoost = int32(100000)

// integrationTimeSharingWindow must tolerate apiserver/client clock skew for "inside window" specs.
const integrationTimeSharingWindow = 60 * time.Second

func TestAPIs(t *testing.T) {
	util.RunSuite(t, "Kueue priority booster suite")
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg) //nolint:fatcontext // Global context is required for test suite setup
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(ctx context.Context, mgr manager.Manager) {
	failedWebhook, err := webhooks.Setup(mgr, nil)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	r := boostctrl.NewPriorityBoostReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorder(boostconstants.ControllerName),
		boostctrl.WithTimeSharingInterval(integrationTimeSharingWindow),
		boostctrl.WithNegativeBoostValue(integrationNegativeBoost),
	)
	gomega.Expect(r.SetupWithManager(mgr)).To(gomega.Succeed())
}
