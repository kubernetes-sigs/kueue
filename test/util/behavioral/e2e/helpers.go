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

	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MustCreate creates a Kubernetes object and panics if it fails
func MustCreate(ctx context.Context, c client.Client, objs ...client.Object) {
	for _, obj := range objs {
		gomega.Expect(c.Create(ctx, obj)).To(gomega.Succeed())
	}
}

// MustUpdate updates a Kubernetes object and panics if it fails
func MustUpdate(ctx context.Context, c client.Client, objs ...client.Object) {
	for _, obj := range objs {
		gomega.Expect(c.Update(ctx, obj)).To(gomega.Succeed())
	}
}

// MustDelete deletes a Kubernetes object and panics if it fails
func MustDelete(ctx context.Context, c client.Client, objs ...client.Object) {
	for _, obj := range objs {
		gomega.Expect(client.IgnoreNotFound(c.Delete(ctx, obj))).To(gomega.Succeed())
	}
}
