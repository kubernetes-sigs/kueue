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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	corecontroller "sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/features"
)

var _ = ginkgo.Describe("LocalQueue metrics configuration", ginkgo.Label("controller:localqueue", "area:core"), func() {
	ginkgo.It("rejects an invalid selector during controller setup", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
		mgr, err := manager.New(cfg, manager.Options{
			Scheme:  k8sClient.Scheme(),
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		invalidCfg := &configapi.Configuration{
			ControllerManager: configapi.ControllerManager{
				Metrics: configapi.ControllerMetrics{
					LocalQueueMetrics: &configapi.LocalQueueMetrics{
						Enable: true,
						LocalQueueSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{Key: "team", Operator: metav1.LabelSelectorOperator("InvalidOp"), Values: []string{"ml"}},
							},
						},
					},
				},
			},
		}
		failedController, err := corecontroller.SetupControllers(mgr, nil, nil, invalidCfg, corecontroller.SetupControllersOpts{})
		gomega.Expect(failedController).To(gomega.Equal("LocalQueue"))
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("metrics.localQueueMetrics.localQueueSelector")))
	})
})
