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
	"errors"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type ObjAsPtr[T any] interface {
	client.Object
	*T
}

func DeleteObject[PtrT ObjAsPtr[T], T any](ctx context.Context, c client.Client, o PtrT) error {
	if o != nil {
		if err := c.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func deleteAllObjectsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, obj client.Object) error {
	err := c.DeleteAllOf(ctx, obj, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, &apimeta.NoKindMatchError{}) {
		return err
	}
	return nil
}

func ExpectObjectToBeDeleted[PtrT ObjAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool) {
	expectObjectToBeDeletedWithTimeout(ctx, k8sClient, o, deleteNow, MediumTimeout)
}

func ExpectObjectToBeDeletedWithTimeout[PtrT ObjAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool, timeout time.Duration) {
	expectObjectToBeDeletedWithTimeout(ctx, k8sClient, o, deleteNow, timeout)
}

func expectObjectToBeDeletedWithTimeout[PtrT ObjAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool, timeout time.Duration) {
	if o == nil {
		return
	}
	if deleteNow {
		gomega.ExpectWithOffset(2, client.IgnoreNotFound(DeleteObject(ctx, k8sClient, o))).To(gomega.Succeed())
	}
	newObj := PtrT(new(T))
	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(o), newObj)).Should(utiltesting.BeNotFoundError())
	}, timeout, Interval).Should(gomega.Succeed(), AssertMsg("Object still exists", newObj))
}

// WaitForNextSecondAfterCreation wait time between the start of the next second
// after creationTimestamp and the current time to ensure that the new
// created object has a later creation time.
func WaitForNextSecondAfterCreation(obj client.Object) {
	time.Sleep(time.Until(obj.GetCreationTimestamp().Add(time.Second)))
}

func MustCreate(ctx context.Context, c client.Client, obj client.Object) {
	ginkgo.GinkgoHelper()
	gomega.Expect(c.Create(ctx, obj)).Should(gomega.Succeed())
}

func MustCreateWithRetry(ctx context.Context, c client.Client, obj client.Object) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(client.IgnoreAlreadyExists(c.Create(ctx, obj))).ToNot(gomega.HaveOccurred())
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to create object after retries", obj))
}

func MustHaveOwnerReference(g gomega.Gomega, ownerRefs []metav1.OwnerReference, obj client.Object, scheme *runtime.Scheme) {
	hasOwnerRef, err := controllerutil.HasOwnerReference(ownerRefs, obj, scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())
	g.Expect(hasOwnerRef).To(gomega.BeTrue())
}

func IgnoreConflict(err error) error {
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func FindDeploymentCondition(deployment *appsv1.Deployment, deploymentType appsv1.DeploymentConditionType) *appsv1.DeploymentCondition {
	for i := range deployment.Status.Conditions {
		c := deployment.Status.Conditions[i]
		if c.Type == deploymentType {
			return &c
		}
	}
	return nil
}

func ResourceQtyToFloat64(quantityStr string) float64 {
	q := resource.MustParse(quantityStr)
	return q.AsApproximateFloat64()
}

func SetNodeCondition(ctx context.Context, k8sClient client.Client, node *corev1.Node, newCondition *corev1.NodeCondition) {
	var updatedNode corev1.Node
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), &updatedNode)).To(gomega.Succeed())
		condition := utiltas.GetNodeCondition(&updatedNode, newCondition.Type)
		changed := false
		if condition == nil {
			updatedNode.Status.Conditions = append(updatedNode.Status.Conditions, *newCondition)
			changed = true
		}
		if newCondition.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = metav1.Now()
			changed = true
		} else if condition.LastTransitionTime != newCondition.LastTransitionTime {
			condition.LastTransitionTime = newCondition.LastTransitionTime
			changed = true
		}
		if condition.Status != newCondition.Status {
			condition.Status = newCondition.Status
			changed = true
		}
		if changed {
			g.Expect(k8sClient.Status().Update(ctx, &updatedNode)).To(gomega.Succeed())
		}
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg(fmt.Sprintf("Failed to set node condition %s to %s for node %s", newCondition.Type, newCondition.Status, node.Name), &updatedNode))
}
