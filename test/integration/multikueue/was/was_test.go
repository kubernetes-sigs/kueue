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

package was

import (
	"github.com/onsi/ginkgo/v2"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/test/integration/multikueue/tas/shared"
)

var _ = func() bool {
	ginkgo.BeforeSuite(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SchedulerLibraryIntegration, true)
	})

	tc := &shared.TestContext{
		ManagerTestCluster: shared.Cluster{
			Cfg:    managerTestCluster.cfg,
			Client: managerTestCluster.client,
			Ctx:    managerTestCluster.ctx,
			Fwk:    managerTestCluster.fwk,
		},
		Worker1TestCluster: shared.Cluster{
			Cfg:    worker1TestCluster.cfg,
			Client: worker1TestCluster.client,
			Ctx:    worker1TestCluster.ctx,
			Fwk:    worker1TestCluster.fwk,
		},
		Worker2TestCluster: shared.Cluster{
			Cfg:    worker2TestCluster.cfg,
			Client: worker2TestCluster.client,
			Ctx:    worker2TestCluster.ctx,
			Fwk:    worker2TestCluster.fwk,
		},
		ManagersConfigNamespace: managersConfigNamespace,
	}
	shared.RunTASIntegrationTests(tc, managerSetup)
	return true
}()
