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

package util

import (
	"fmt"
	"strings"

	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/types"
	eventsv1 "k8s.io/api/events/v1"
)

func haveEvent(expectedEvent eventsv1.Event) types.GomegaMatcher {
	return &eventMatcher{expectedEvent: expectedEvent}
}

type eventMatcher struct {
	expectedEvent eventsv1.Event
}

func (matcher *eventMatcher) Match(actual any) (bool, error) {
	events, ok := actual.([]eventsv1.Event)
	if !ok {
		return false, fmt.Errorf("event matcher expects a []eventsv1.Event. Got:\n%s", format.Object(actual, 1))
	}
	for i := range events {
		if events[i].Reason == matcher.expectedEvent.Reason && events[i].Type == matcher.expectedEvent.Type && events[i].Note == matcher.expectedEvent.Note {
			return true, nil
		}
	}
	return false, nil
}

func (matcher *eventMatcher) FailureMessage(actual any) string {
	return matcher.buildErrorMessage(actual, false)
}

func (matcher *eventMatcher) NegatedFailureMessage(actual any) string {
	return matcher.buildErrorMessage(actual, true)
}

func (matcher *eventMatcher) buildErrorMessage(actual any, negated bool) string {
	b := strings.Builder{}
	b.WriteString("Expected\n")
	b.WriteString(format.Object(actual, 1))
	b.WriteByte('\n')
	if negated {
		b.WriteString("not ")
	}
	fmt.Fprintf(&b, "to contain an Event matching Reason %q, Type %q, and Note %q", matcher.expectedEvent.Reason, matcher.expectedEvent.Type, matcher.expectedEvent.Note)
	return b.String()
}
