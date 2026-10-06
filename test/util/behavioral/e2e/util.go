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
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

func ExpectNodeToBecomeReady(ctx context.Context, c client.Client, nodeName string, localQueue *kueue.LocalQueue) {
	ginkgo.GinkgoHelper()

	node := &corev1.Node{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKey{Name: nodeName}, node)).To(gomega.Succeed())
		g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg(fmt.Sprintf("Node %s did not become Ready", nodeName), node))

	waitForDummyWorkloadToRunOnNode(ctx, c, node, localQueue)
}

func waitForDummyWorkloadToRunOnNode(ctx context.Context, c client.Client, node *corev1.Node, lq *kueue.LocalQueue) {
	ginkgo.GinkgoHelper()

	ginkgo.By(fmt.Sprintf("Waiting for a dummy workload to run on the recovered node %s", node.Name), func() {
		dummyJob := testingjob.MakeJob(fmt.Sprintf("dummy-job-%s", node.Name), lq.Namespace).
			Queue(kueue.LocalQueueName(lq.Name)).
			NodeSelector(corev1.LabelHostname, node.Name).
			Image(GetAgnHostImage(), BehaviorExitFast).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			// we just need to test that the Node allows to run Pods already, using two Pods to indroduce extra redundancy
			Parallelism(2).
			Completions(2).
			CompletionMode(batchv1.IndexedCompletion).
			SuccessPolicy(&batchv1.SuccessPolicy{
				Rules: []batchv1.SuccessPolicyRule{
					{
						SucceededCount: new(int32(1)),
					},
				},
			}).
			Obj()

		behavioral.MustCreate(ctx, c, dummyJob)

		var createdDummyJob batchv1.Job
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(dummyJob), &createdDummyJob)).To(gomega.Succeed())
			g.Expect(createdDummyJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}, cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg(fmt.Sprintf("Dummy workload did not complete on node %s", node.Name), &createdDummyJob))
	})
}
