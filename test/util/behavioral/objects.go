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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testutil "sigs.k8s.io/kueue/test/util"
)

type objAsPtr[T any] interface {
	client.Object
	*T
}

// DeleteObject deletes a Kubernetes object
func DeleteObject[PtrT objAsPtr[T], T any](ctx context.Context, c client.Client, o PtrT) error {
	if o != nil {
		if err := c.Delete(ctx, o); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// ExpectObjectToBeDeleted waits until an object is deleted (with default timeout)
func ExpectObjectToBeDeleted[PtrT objAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool) {
	expectObjectToBeDeletedWithTimeout(ctx, k8sClient, o, deleteNow, MediumTimeout)
}

// ExpectObjectToBeDeletedWithTimeout waits until an object is deleted (with custom timeout)
func ExpectObjectToBeDeletedWithTimeout[PtrT objAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool, timeout time.Duration) {
	expectObjectToBeDeletedWithTimeout(ctx, k8sClient, o, deleteNow, timeout)
}

func expectObjectToBeDeletedWithTimeout[PtrT objAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool, timeout time.Duration) {
	if o == nil {
		return
	}
	if deleteNow {
		gomega.ExpectWithOffset(2, client.IgnoreNotFound(DeleteObject(ctx, k8sClient, o))).To(gomega.Succeed())
	}
	newObj := PtrT(new(T))
	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(o), newObj)).Should(utiltesting.BeNotFoundError())
	}, timeout, Interval).Should(gomega.Succeed(), testutil.AssertMsg("Object still exists", newObj))
}

// ExpectObjectToBeDeletedOnClusters waits until object is deleted on all provided clients
func ExpectObjectToBeDeletedOnClusters[PtrT objAsPtr[T], T any](ctx context.Context, obj PtrT, clients ...client.Client) {
	ginkgo.GinkgoHelper()
	if len(clients) == 0 {
		ginkgo.Fail("At least one client must be provided to check for object deletion")
	}
	for _, c := range clients {
		ExpectObjectToBeDeleted(ctx, c, obj, false)
	}
}
