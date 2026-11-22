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

package dqo

import (
	"strings"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/api"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
)

func TestSetDistributionConditionTruncatesMessage(t *testing.T) {
	// TruncateConditionMessage uses Go string length (bytes), matching the 32KiB
	// metav1.Condition.Message limit in pkg/util/api.maxConditionMsgSize.
	const maxConditionMsgSize = 32 * 1024
	const generation int64 = 7
	status := metav1.ConditionFalse
	reason := kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured

	cases := map[string]struct {
		message string
	}{
		"short message is unchanged": {
			message: "Quotas successfully distributed",
		},
		"exact 32KiB byte limit is unchanged": {
			message: strings.Repeat("a", maxConditionMsgSize),
		},
		"message longer than 32KiB is truncated": {
			message: strings.Repeat("a", maxConditionMsgSize+128),
		},
		"multibyte message under the byte limit is unchanged": {
			// "世" is 3 bytes in UTF-8; 100 runes = 300 bytes, well under the limit.
			message: strings.Repeat("世", 100),
		},
		"multibyte message over the byte limit is truncated by bytes": {
			// 10923 * 3 = 32769 bytes, one byte over the limit.
			// truncateMessage slices by len() (bytes), not runes.
			message: strings.Repeat("世", 10923),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			orchestrator := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").
				Generation(generation).
				Obj()
			r := &Reconciler{}
			r.setDistributionCondition(orchestrator, status, reason, tc.message)

			got := apimeta.FindStatusCondition(orchestrator.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
			if got == nil {
				t.Fatal("Distributed condition was not set")
			}
			if got.Status != status || got.Reason != reason || got.ObservedGeneration != generation {
				t.Errorf("unexpected condition metadata: status=%s reason=%s observedGeneration=%d", got.Status, got.Reason, got.ObservedGeneration)
			}
			wantMessage := api.TruncateConditionMessage(tc.message)
			if got.Message != wantMessage {
				t.Errorf("message was not passed through api.TruncateConditionMessage: gotLen=%d wantLen=%d equalsInput=%t",
					len(got.Message), len(wantMessage), got.Message == tc.message)
			}
		})
	}
}
