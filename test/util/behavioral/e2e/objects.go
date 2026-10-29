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

package e2e

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/util/behavioral"
)

func GetListOptsFromLabel(label string) *client.ListOptions {
	ginkgo.GinkgoHelper()
	selector, err := labels.Parse(label)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return &client.ListOptions{
		LabelSelector: selector,
	}
}

func ExpectObjectToBeDeletedOnClusters[PtrT behavioral.ObjAsPtr[T], T any](ctx context.Context, obj PtrT, clients ...client.Client) {
	ginkgo.GinkgoHelper()
	if len(clients) == 0 {
		ginkgo.Fail("At least one client must be provided to check for object deletion")
	}
	for _, c := range clients {
		behavioral.ExpectObjectToBeDeleted(ctx, c, obj, false)
	}
}
