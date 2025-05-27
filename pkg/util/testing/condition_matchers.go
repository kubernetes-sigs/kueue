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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func HaveConditionStatusAndReason(conditionType string, status metav1.ConditionStatus, reason string) types.GomegaMatcher {
	if conditionType == "" {
		panic("conditionType is empty")
	}
	return &conditionMatcher{
		conditionType: conditionType,
		status:        status,
		reason:        reason,
	}
}

func HaveConditionStatus(conditionType string, status metav1.ConditionStatus) types.GomegaMatcher {
	return HaveConditionStatusAndReason(conditionType, status, "")
}

func HaveCondition(conditionType string) types.GomegaMatcher {
	return HaveConditionStatusAndReason(conditionType, "", "")
}

func HaveConditionStatusTrue(conditionType string) types.GomegaMatcher {
	return HaveConditionStatus(conditionType, metav1.ConditionTrue)
}

func HaveConditionStatusTrueAndReason(conditionType, reason string) types.GomegaMatcher {
	return HaveConditionStatusAndReason(conditionType, metav1.ConditionTrue, reason)
}

func HaveConditionStatusFalse(conditionType string) types.GomegaMatcher {
	return HaveConditionStatus(conditionType, metav1.ConditionFalse)
}

func HaveConditionStatusFalseAndReason(conditionType string, reason string) types.GomegaMatcher {
	return HaveConditionStatusAndReason(conditionType, metav1.ConditionFalse, reason)
}

type conditionMatcher struct {
	conditionType string
	status        metav1.ConditionStatus
	reason        string
}

func (matcher *conditionMatcher) Match(actual any) (bool, error) {
	conditions, ok := actual.([]metav1.Condition)
	if !ok {
		return false, fmt.Errorf("Condition matcher expects a []metav1.Condition. Got:\n%s", format.Object(actual, 1))
	}

	found := apimeta.FindStatusCondition(conditions, matcher.conditionType)
	if found == nil {
		return false, nil
	}

	if matcher.status != "" && found.Status != matcher.status {
		return false, nil
	}

	if matcher.reason != "" && found.Reason != matcher.reason {
		return false, nil
	}

	return true, nil
}

func (matcher *conditionMatcher) FailureMessage(actual any) string {
	return matcher.buildErrorMessage(actual, false)
}

func (matcher *conditionMatcher) NegatedFailureMessage(actual any) string {
	return matcher.buildErrorMessage(actual, true)
}

func (matcher *conditionMatcher) buildErrorMessage(actual any, negated bool) string {
	b := strings.Builder{}
	b.WriteString("Expected\n")
	b.WriteString(format.Object(actual, 1))
	b.WriteByte('\n')
	if negated {
		b.WriteString("not ")
	}
	b.WriteString("to have condition type ")
	b.WriteString(matcher.conditionType)
	if matcher.status != "" {
		if matcher.reason == "" {
			b.WriteString(" and")
		} else {
			b.WriteByte(',')
		}
		b.WriteString(" status ")
		b.WriteString(string(matcher.status))
	}
	if matcher.reason != "" {
		b.WriteString(" and reason ")
		b.WriteString(matcher.reason)
	}
	return b.String()
}
