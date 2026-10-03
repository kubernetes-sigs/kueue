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

	"github.com/onsi/gomega"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// FindStatusCondition finds a condition by type in conditions list
func FindStatusCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	return apimeta.FindStatusCondition(conditions, condType)
}

// HaveConditionStatusTrue returns a matcher for checking condition status is true
func HaveConditionStatusTrue(condType string) gomega.OmegaMatcher {
	return utiltesting.HaveConditionStatusTrue(condType)
}

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
