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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func DeleteAllEventsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &eventsv1.Event{})
}

// ExpectEventAppeared asserts that an event matching Reason/Type/Note has been emitted.
func ExpectEventAppeared(ctx context.Context, k8sClient client.Client, event eventsv1.Event) {
	ginkgo.GinkgoHelper()
	observedEvents := &eventsv1.EventList{}
	gomega.Eventually(func(g gomega.Gomega) {
		observedEvents.Items = nil
		err := k8sClient.List(ctx, observedEvents)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(observedEvents.Items).To(haveEvent(event))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// EventsForObject lists the events regarding the object identified by key.
func EventsForObject(ctx context.Context, k8sClient client.Client, key types.NamespacedName) ([]eventsv1.Event, error) {
	events := &eventsv1.EventList{}
	if err := k8sClient.List(ctx, events, client.InNamespace(key.Namespace)); err != nil {
		return nil, err
	}
	var result []eventsv1.Event
	for _, event := range events.Items {
		if event.Regarding.Namespace == key.Namespace && event.Regarding.Name == key.Name {
			result = append(result, event)
		}
	}
	return result, nil
}
