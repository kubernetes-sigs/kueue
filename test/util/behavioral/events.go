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
	eventsv1 "k8s.io/api/events/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ExpectEventWithMessage waits until an event with specific message is recorded
func ExpectEventWithMessage(
	ctx context.Context,
	k8sClient client.Client,
	namespace string,
	involvedObjectName string,
	eventMessage string,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		eventList := &eventsv1.EventList{}
		g.Expect(k8sClient.List(ctx, eventList, client.InNamespace(namespace))).To(gomega.Succeed())

		found := false
		for _, event := range eventList.Items {
			if event.Regarding.Name == involvedObjectName && event.Note == eventMessage {
				found = true
				break
			}
		}
		g.Expect(found).To(gomega.BeTrue(), fmt.Sprintf("Event with message %q not found for object %q", eventMessage, involvedObjectName))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectEventWithReason waits until an event with specific reason is recorded
func ExpectEventWithReason(
	ctx context.Context,
	k8sClient client.Client,
	namespace string,
	involvedObjectName string,
	reason string,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		eventList := &eventsv1.EventList{}
		g.Expect(k8sClient.List(ctx, eventList, client.InNamespace(namespace))).To(gomega.Succeed())

		found := false
		for _, event := range eventList.Items {
			if event.Regarding.Name == involvedObjectName && event.Reason == reason {
				found = true
				break
			}
		}
		g.Expect(found).To(gomega.BeTrue(), fmt.Sprintf("Event with reason %q not found for object %q", reason, involvedObjectName))
	}, Timeout, Interval).Should(gomega.Succeed())
}
