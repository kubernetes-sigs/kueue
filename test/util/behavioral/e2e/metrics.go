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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/client-go/rest"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

const defaultMetricsServiceName = "kueue-controller-manager-metrics-service"

func ExpectPrometheusTargetForKueue(ctx context.Context, prometheusClient prometheusv1.API) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		result, err := prometheusClient.Targets(ctx)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		hasKueueTarget := false
		for _, t := range result.Active {
			if t.Labels["job"] == defaultMetricsServiceName &&
				t.Labels["namespace"] == model.LabelValue(GetKueueNamespace()) {
				hasKueueTarget = true
				g.Expect(t.Health).To(gomega.Equal(prometheusv1.HealthGood))
				break
			}
		}
		g.Expect(hasKueueTarget).To(gomega.BeTrue(), "Kueue target not found. Active targets: %v", result.Active)
	}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
}

// GetKueueMetrics scrapes the Kueue metrics endpoint from the given curl pod, returning the
// response body and curl's stderr.
//
// The fetch is bounded because callers poll this helper from an Eventually block, which
// cannot interrupt a call that is already in flight. Left unbounded, curl falls back to its
// built-in 300s connection timeout - far longer than the LongTimeout budget the callers poll
// with - so a single stalled fetch consumes the whole budget and is never retried.
// --fail keeps a failed response on that same retry path: ExcludeMetrics matches vacuously
// against an error body, so an unauthorized response would otherwise be accepted as proof
// that the metrics are gone.
func GetKueueMetrics(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string) (string, string, error) {
	kueueNS := GetKueueNamespace()
	ctx, cancel := context.WithTimeout(ctx, behavioral.MediumTimeout)
	defer cancel()
	metricsOutput, stderr, err := KExecute(ctx, cfg, restClient, kueueNS, curlPodName, curlContainerName, []string{
		"/bin/sh", "-c",
		fmt.Sprintf(
			"curl -sS --fail --connect-timeout 5 --max-time 15 -k -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics",
			defaultMetricsServiceName, kueueNS,
		),
	})
	return string(metricsOutput), string(stderr), err
}

func ExpectMetricsToBeAvailable(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string, metrics [][]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, stderr, err := GetKueueMetrics(ctx, cfg, restClient, curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", stderr)
		g.Expect(metricsOutput).Should(utiltesting.ContainMetrics(metrics))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
}

func ExpectMetricsNotToBeAvailable(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string, metrics [][]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, stderr, err := GetKueueMetrics(ctx, cfg, restClient, curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", stderr)
		g.Expect(metricsOutput).Should(utiltesting.ExcludeMetrics(metrics))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
}
