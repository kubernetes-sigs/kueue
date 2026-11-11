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

package tas

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Topology validations", func() {
	ginkgo.When("Creating a Topology", func() {
		ginkgo.DescribeTable("Validate Topology on creation", func(topology *kueue.Topology, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, topology)
			if err == nil {
				defer func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("valid Topology",
				utiltestingapi.MakeDefaultOneLevelTopology("valid"),
				gomega.Succeed()),
			ginkgo.Entry("no levels",
				utiltestingapi.MakeTopology("no-levels").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid levels",
				utiltestingapi.MakeTopology("invalid-level").Levels("@invalid").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("non-unique levels",
				utiltestingapi.MakeTopology("default").Levels(utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultBlockTopologyLevel).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname first",
				utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname, utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname middle",
				utiltestingapi.MakeTopology("default").Levels(utiltesting.DefaultBlockTopologyLevel, corev1.LabelHostname, utiltesting.DefaultRackTopologyLevel).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname last",
				utiltestingapi.MakeTopology("default").Levels(utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel, corev1.LabelHostname).Obj(),
				gomega.Succeed()),
		)
	})

	ginkgo.When("Updating a Topology", func() {
		ginkgo.DescribeTable("Validate Topology on update", func(topology *kueue.Topology, updateTopology func(topology *kueue.Topology), matcher types.GomegaMatcher) {
			behavioral.MustCreate(ctx, k8sClient, topology)
			defer func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			}()
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), topology)).Should(gomega.Succeed())
			updateTopology(topology)
			gomega.Expect(k8sClient.Update(ctx, topology)).Should(matcher)
		},
			ginkgo.Entry("succeed to update topology",
				utiltestingapi.MakeDefaultOneLevelTopology("valid"),
				func(topology *kueue.Topology) {
					topology.Labels = map[string]string{
						"alpha": "beta",
					}
				},
				gomega.Succeed()),
			ginkgo.Entry("appending a level below hostname is prohibited",
				utiltestingapi.MakeDefaultOneLevelTopology("valid"),
				func(topology *kueue.Topology) {
					topology.Spec.Levels = append(topology.Spec.Levels, kueue.TopologyLevel{
						NodeLabel: "added",
					})
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("reordering levels above hostname is allowed",
				utiltestingapi.MakeDefaultThreeLevelTopology("default"),
				func(topology *kueue.Topology) {
					topology.Spec.Levels[0], topology.Spec.Levels[1] = topology.Spec.Levels[1], topology.Spec.Levels[0]
				},
				gomega.Succeed()),
			ginkgo.Entry("moving hostname away from the lowest level is prohibited",
				utiltestingapi.MakeDefaultThreeLevelTopology("default"),
				func(topology *kueue.Topology) {
					topology.Spec.Levels[1], topology.Spec.Levels[2] = topology.Spec.Levels[2], topology.Spec.Levels[1]
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("updating levels of a topology without hostname is prohibited",
				utiltestingapi.MakeDefaultTwoLevelTopology("valid"),
				func(topology *kueue.Topology) {
					topology.Spec.Levels[0].NodeLabel = "changed"
				},
				utiltesting.BeInvalidError()),
		)
	})
})
