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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// BindUserToClusterRole binds user to clusterRole and returns a clientset acting as that user,
// once probe confirms the binding is in effect. The binding is removed when the spec ends.
//
// The binding is cluster-wide so it can grant access to cluster-scoped resources: a RoleBinding
// would deny access on scope alone, making Forbidden assertions pass for the wrong reason.
func BindUserToClusterRole(ctx context.Context, c client.Client, user, clusterRole string,
	probe func(context.Context, kueueclientset.Interface) error) kueueclientset.Interface {
	ginkgo.GinkgoHelper()

	binding := utiltesting.MakeClusterRoleBinding(user+"-binding").
		RoleRef(rbacv1.GroupName, "ClusterRole", clusterRole).
		UserSubject(user).
		Obj()
	behavioral.MustCreate(ctx, c, binding)
	// Registered before the gate below: a gate failure must not leak this cluster-scoped object,
	// or the next run collides with it on create.
	ginkgo.DeferCleanup(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, c, binding, true)
	})

	clientset := CreateKueueClientset(user)
	ginkgo.By("Wait for an already granted request to succeed to make sure the role binding is in effect", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(probe(ctx, clientset)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
	return clientset
}

// ListLocalQueues lists LocalQueues across all namespaces. Use it as the BindUserToClusterRole
// probe for roles that grant more than the resource under test: probing the permission under
// test would report a real regression as an opaque timeout.
func ListLocalQueues(ctx context.Context, c kueueclientset.Interface) error {
	_, err := c.KueueV1beta2().LocalQueues(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	return err
}
