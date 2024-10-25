/*
Copyright 2024 The Kubernetes Authors.

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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func HaveCondition(condition string) types.GomegaMatcher {
	return &conditionMatcher{
		condition: condition,
	}
}

func HaveConditionStatus(condition string, status metav1.ConditionStatus) types.GomegaMatcher {
	return &conditionMatcher{
		condition: condition,
		status:    status,
	}
}

func HaveConditionStatusTrue(condition string) types.GomegaMatcher {
	return HaveConditionStatus(condition, metav1.ConditionTrue)
}

func HaveConditionStatusFalse(condition string) types.GomegaMatcher {
	return HaveConditionStatus(condition, metav1.ConditionFalse)
}

type conditionMatcher struct {
	condition string
	status    metav1.ConditionStatus
}

func (matcher *conditionMatcher) Match(actual interface{}) (bool, error) {
	conditions, ok := actual.([]metav1.Condition)
	if !ok {
		return false, fmt.Errorf("Condition matcher expects a []metav1.Condition. Got:\n%s", format.Object(actual, 1))
	}

	found := apimeta.FindStatusCondition(conditions, matcher.condition)
	if found == nil {
		return false, nil
	}

	if matcher.status == "" {
		return true, nil
	}

	return found.Status == matcher.status, nil
}

func (matcher *conditionMatcher) FailureMessage(actual interface{}) string {
	return matcher.buildErrorMessage(actual, false)
}

func (matcher *conditionMatcher) NegatedFailureMessage(actual interface{}) string {
	return matcher.buildErrorMessage(actual, true)
}

func (matcher *conditionMatcher) buildErrorMessage(actual interface{}, negated bool) string {
	b := strings.Builder{}
	b.WriteString("Expected\n")
	b.WriteString(format.Object(actual, 1))
	b.WriteString("\n")
	if negated {
		b.WriteString("not ")
	}
	b.WriteString("to have condition ")
	b.WriteString(matcher.condition)
	if matcher.status != "" {
		b.WriteString(" and status ")
		b.WriteString(string(matcher.status))
	}
	return b.String()
}
