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

package testing

import (
	"fmt"
	"strings"

	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/types"

	utilstrings "sigs.k8s.io/kueue/pkg/util/strings"
)

type mode string

const (
	containsAll mode = "contains-all"
	excludesAll mode = "excludes-all"
)

func ContainMetrics(expectedMetrics [][]string) types.GomegaMatcher {
	return &metricsMatcher{
		expectedMetrics: expectedMetrics,
		mode:            containsAll,
	}
}

func ExcludeMetrics(expectedMetrics [][]string) types.GomegaMatcher {
	return &metricsMatcher{
		expectedMetrics: expectedMetrics,
		mode:            excludesAll,
	}
}

type metricsMatcher struct {
	mode             mode
	expectedMetrics  [][]string
	lastFailedMetric []string
}

func containsMetric(output []string, metric []string) bool {
	for _, line := range output {
		if utilstrings.StringContainsSubstrings(line, metric...) {
			return true
		}
	}

	return false
}

func (matcher *metricsMatcher) Match(actual any) (bool, error) {
	input, ok := actual.(string)
	if !ok {
		//nolint:staticcheck // We keep the error capitalized for consistency with built-in matchers.
		return false, fmt.Errorf("Metrics matcher expects a string. Got:\n%s", format.Object(actual, 1))
	}

	lines := strings.Split(input, "\n")

	var assertFunction func(output []string, metric []string) bool

	switch matcher.mode {
	case containsAll:
		assertFunction = containsMetric
	case excludesAll:
		assertFunction = func(output []string, metric []string) bool {
			return !containsMetric(output, metric)
		}
	default:
		return false, fmt.Errorf("unsupported matcher mode: %s", matcher.mode)
	}

	for _, metric := range matcher.expectedMetrics {
		if !assertFunction(lines, metric) {
			matcher.lastFailedMetric = metric

			return false, nil
		}
	}

	return true, nil
}

func filterOutputWithKueueMetrics(input string) string {
	lines := strings.Split(input, "\n")

	output := make([]string, 0, len(lines))

	for _, line := range lines {
		if strings.HasPrefix(line, "kueue_") {
			output = append(output, line)
		}
	}

	return strings.Join(output, "\n")
}

func actualToOutput(actual any) any {
	if actualStr, ok := actual.(string); ok {
		return filterOutputWithKueueMetrics(actualStr)
	}

	return actual
}

func (matcher *metricsMatcher) FailureMessage(actual any) string {
	var messageIntro string
	switch matcher.mode {
	case containsAll:
		messageIntro = fmt.Sprintf(
			"Expected to contain metric:\n%s\n",
			format.Object(matcher.lastFailedMetric, 1),
		)
	case excludesAll:
		messageIntro = fmt.Sprintf(
			"Expected not to contain metric:\n%s\n",
			format.Object(matcher.lastFailedMetric, 1),
		)
	default:
		return fmt.Sprintf("unsupported matcher mode: %s", matcher.mode)
	}

	return fmt.Sprintf(
		"%s\nActual:\n%s",
		messageIntro,
		format.Object(actualToOutput(actual), 1),
	)
}

func (matcher *metricsMatcher) NegatedFailureMessage(_ any) string {
	panic("metricsMatcher does not support negated testing. Please implement if needed.")
}
