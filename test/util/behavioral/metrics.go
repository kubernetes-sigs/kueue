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

package behavioral

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// ExpectMetricsContainValue checks if a metric has a specific value
func ExpectMetricsContainValue(
	ctx context.Context,
	metricsContent string,
	metricName string,
	expectedValue string,
) {
	ginkgo.GinkgoHelper()
	gomega.Expect(metricsContent).To(gomega.ContainSubstring(fmt.Sprintf("%s %s", metricName, expectedValue)))
}

// ExpectMetricsNotContainMetric checks if a metric is NOT present
func ExpectMetricsNotContainMetric(
	ctx context.Context,
	metricsContent string,
	metricName string,
) {
	ginkgo.GinkgoHelper()
	gomega.Expect(metricsContent).NotTo(gomega.ContainSubstring(metricName))
}
