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
	"sigs.k8s.io/kueue/test/integration/singlecluster/tas/shared"
)

var _ = func() bool {
	ginkgo.BeforeSuite(func() {
		// Enable the scheduler-library feature gate for the entire WAS suite
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SchedulerLibraryIntegration, true)
	})

	tc := &shared.TestContext{
		Ctx:       ctx,
		K8sClient: k8sClient,
		Fwk:       fwk,
		Cfg:       cfg,
		QManager:  qManager,
	}
	shared.RunTASIntegrationTests(tc, managerSetup, managerSetupWithConfig)
	return true
}()
