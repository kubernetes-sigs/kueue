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
	"k8s.io/client-go/rest"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	DefaultMetricsServiceName = "kueue-metrics"
)

// GetKueueMetrics scrapes the Kueue metrics endpoint from the given curl pod
// returning the response body and curl's stderr.
func GetKueueMetrics(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string) (string, string, error) {
	kueueNS := GetKueueNamespace()
	ctx, cancel := context.WithTimeout(ctx, MediumTimeout)
	defer cancel()
	metricsOutput, stderr, err := KExecute(ctx, cfg, restClient, kueueNS, curlPodName, curlContainerName, []string{
		"/bin/sh", "-c",
		fmt.Sprintf(
			"curl -sS --fail --connect-timeout 5 --max-time 15 -k -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics",
			DefaultMetricsServiceName, kueueNS,
		),
	})
	return string(metricsOutput), string(stderr), err
}

// ExpectMetricsToBeAvailable waits for specific metrics to be available
func ExpectMetricsToBeAvailable(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string, metrics [][]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, stderr, err := GetKueueMetrics(ctx, cfg, restClient, curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", stderr)
		g.Expect(metricsOutput).Should(utiltesting.ContainMetrics(metrics))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

// ExpectMetricsNotToBeAvailable waits for specific metrics to NOT be available
func ExpectMetricsNotToBeAvailable(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, curlPodName, curlContainerName string, metrics [][]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, stderr, err := GetKueueMetrics(ctx, cfg, restClient, curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", stderr)
		g.Expect(metricsOutput).Should(utiltesting.ExcludeMetrics(metrics))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}
