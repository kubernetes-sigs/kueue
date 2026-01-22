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
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func ExpectEventsForObjectsWithTimeout(eventWatcher watch.Interface, objs sets.Set[types.NamespacedName], filter func(*corev1.Event) bool, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gotObjs := sets.New[types.NamespacedName]()
	timeoutCh := time.After(timeout)
readCh:
	for !gotObjs.Equal(objs) {
		select {
		case evt, ok := <-eventWatcher.ResultChan():
			gomega.Expect(ok).To(gomega.BeTrue())
			event, ok := evt.Object.(*corev1.Event)
			gomega.Expect(ok).To(gomega.BeTrue())
			if filter(event) {
				objKey := types.NamespacedName{Namespace: event.InvolvedObject.Namespace, Name: event.InvolvedObject.Name}
				gotObjs.Insert(objKey)
			}
		case <-timeoutCh:
			break readCh
		}
	}
	gomega.Expect(gotObjs).To(gomega.Equal(objs))
}

// ExpectEventAppeared asserts that an event matching Reason/Type/Message has been emitted.
func ExpectEventAppeared(ctx context.Context, k8sClient client.Client, event corev1.Event) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		ok, err := utiltesting.HasEventAppeared(ctx, k8sClient, event)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(ok).Should(gomega.BeTrue())
	}, Timeout, Interval).Should(gomega.Succeed())
}
