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

package core

import (
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	multiKueueClusterLocationMaxLength           = 256
	multiKueueClusterProfileReferenceMaxLength   = 256
	multiKueueClusterStatusConditionsMaxItems    = 16
	multiKueueClusterStatusConditionReasonPrefix = "TestReason"
)

var _ = ginkgo.Describe("MultiKueueCluster Webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.When("Creating a MultiKueueCluster", func() {
		ginkgo.DescribeTable("Defaulting on creation", func(mkc, wantMKC kueue.MultiKueueCluster) {
			util.MustCreate(ctx, k8sClient, &mkc)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &mkc, true)
			}()
			gomega.Expect(mkc).To(gomega.BeComparableTo(wantMKC,
				cmpopts.IgnoreTypes(kueue.MultiKueueClusterStatus{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields")))
		},
			ginkgo.Entry("KubeConfig locationType defaults to Secret",
				*utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig("", "worker-secret").Obj(),
				*utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig(kueue.SecretLocationType, "worker-secret").Obj(),
			),
		)

		ginkgo.DescribeTable("Validate MultiKueueCluster on creation", func(mkc client.Object, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, mkc)
			if err == nil {
				defer func() {
					gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, mkc))).To(gomega.Succeed())
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("Should allow KubeConfig with Secret location type",
				utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig(kueue.SecretLocationType, "worker-secret").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow KubeConfig with Path location type",
				utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig(kueue.PathLocationType, "/etc/kueue/worker.kubeconfig").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow ClusterProfileRef",
				utiltestingapi.MakeMultiKueueCluster("worker").ClusterProfile("worker-profile").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject missing cluster source",
				makeMultiKueueClusterWithSpec("worker", map[string]any{}),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject both KubeConfig and ClusterProfileRef",
				utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig(kueue.SecretLocationType, "worker-secret").ClusterProfile("worker-profile").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject empty KubeConfig location",
				utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig(kueue.SecretLocationType, "").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject too long KubeConfig location",
				utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig(kueue.SecretLocationType, strings.Repeat("a", multiKueueClusterLocationMaxLength+1)).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject invalid KubeConfig location type",
				utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig("Invalid", "worker-secret").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject empty ClusterProfileRef name",
				utiltestingapi.MakeMultiKueueCluster("worker").ClusterProfile("").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject too long ClusterProfileRef name",
				utiltestingapi.MakeMultiKueueCluster("worker").ClusterProfile(strings.Repeat("a", multiKueueClusterProfileReferenceMaxLength+1)).Obj(),
				utiltesting.BeInvalidError()),
		)
	})

	ginkgo.When("Updating a MultiKueueCluster status", func() {
		ginkgo.DescribeTable("Validate status conditions on update", func(conditionCount int, matcher types.GomegaMatcher) {
			mkc := utiltestingapi.MakeMultiKueueCluster("worker").KubeConfig(kueue.SecretLocationType, "worker-secret").Obj()
			util.MustCreate(ctx, k8sClient, mkc)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, mkc, true)
			}()

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mkc), mkc)).To(gomega.Succeed())
			mkc.Status.Conditions = makeConditions(conditionCount)
			gomega.Expect(k8sClient.Status().Update(ctx, mkc)).Should(matcher)
		},
			ginkgo.Entry("Should allow maximum number of conditions",
				multiKueueClusterStatusConditionsMaxItems,
				gomega.Succeed()),
			ginkgo.Entry("Should reject too many conditions",
				multiKueueClusterStatusConditionsMaxItems+1,
				utiltesting.BeInvalidError()),
		)
	})
})

func makeConditions(count int) []metav1.Condition {
	conditions := make([]metav1.Condition, count)
	for i := range conditions {
		conditions[i] = metav1.Condition{
			Type:               fmt.Sprintf("Condition%d", i),
			Status:             metav1.ConditionTrue,
			Reason:             fmt.Sprintf("%s%d", multiKueueClusterStatusConditionReasonPrefix, i),
			Message:            "test condition",
			LastTransitionTime: metav1.Now(),
		}
	}
	return conditions
}

func makeMultiKueueClusterWithSpec(name string, spec map[string]any) *unstructured.Unstructured {
	mkc := &unstructured.Unstructured{}
	mkc.SetAPIVersion(kueue.GroupVersion.String())
	mkc.SetKind("MultiKueueCluster")
	mkc.SetName(name)
	mkc.Object["spec"] = spec
	return mkc
}
