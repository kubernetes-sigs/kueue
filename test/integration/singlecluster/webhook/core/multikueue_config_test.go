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

package core

import (
	"fmt"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	multiKueueConfigClustersMaxItems = 20
	multiKueueConfigClusterMaxLength = 256
)

var _ = ginkgo.Describe("MultiKueueConfig Webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.When("Creating a MultiKueueConfig", func() {
		ginkgo.DescribeTable("Validate MultiKueueConfig on creation", func(mkc *kueue.MultiKueueConfig, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, mkc)
			if err == nil {
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, mkc, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("Should allow one cluster",
				utiltestingapi.MakeMultiKueueConfig("multikueue-config").Clusters("worker1").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow multiple clusters",
				utiltestingapi.MakeMultiKueueConfig("multikueue-config").Clusters("worker1", "worker2").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject missing clusters",
				utiltestingapi.MakeMultiKueueConfig("multikueue-config").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject duplicate clusters",
				utiltestingapi.MakeMultiKueueConfig("multikueue-config").Clusters("worker1", "worker1").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject too many clusters",
				func() *kueue.MultiKueueConfig {
					clusters := make([]string, multiKueueConfigClustersMaxItems+1)
					for i := range clusters {
						clusters[i] = fmt.Sprintf("worker%d", i)
					}
					return utiltestingapi.MakeMultiKueueConfig("multikueue-config").Clusters(clusters...).Obj()
				}(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject too long cluster name",
				utiltestingapi.MakeMultiKueueConfig("multikueue-config").Clusters(strings.Repeat("a", multiKueueConfigClusterMaxLength+1)).Obj(),
				utiltesting.BeInvalidError()),
		)
	})
})
