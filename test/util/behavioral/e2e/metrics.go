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

package e2e

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"sigs.k8s.io/kueue/test/util/behavioral"
)

func ExpectPrometheusTargetForKueue(ctx context.Context, prometheusClient prometheusv1.API) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		result, err := prometheusClient.Targets(ctx)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		hasKueueTarget := false
		for _, t := range result.Active {
			if t.Labels["job"] == DefaultMetricsServiceName &&
				t.Labels["namespace"] == model.LabelValue(GetKueueNamespace()) {
				hasKueueTarget = true
				g.Expect(t.Health).To(gomega.Equal(prometheusv1.HealthGood))
				break
			}
		}
		g.Expect(hasKueueTarget).To(gomega.BeTrue(), "Kueue target not found. Active targets: %v", result.Active)
	}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
}
