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

package tas

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func setupTest(clock clock.Clock, initObjs ...client.Object) (client.Client, *nodeFailureReconciler) {
	s := scheme.Scheme
	if err := kueue.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}

	clientBuilder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(initObjs...).
		WithStatusSubresource(initObjs...)

	err := indexer.SetupIndexes(context.Background(), utiltesting.AsIndexer(clientBuilder))
	if err != nil {
		panic(fmt.Sprintf("Failed to setup indexes: %v", err))
	}
	cl := clientBuilder.Build()
	r := newNodeFailureReconciler(cl)
	r.clock = clock

	return cl, r
}

// newNodeTest is a helper to create a Node object for testing
func newNodeTest(name string, readyStatus corev1.ConditionStatus, clock clock.Clock, transitionTimeOffset time.Duration) *corev1.Node {
	return testingnode.MakeNode(name).StatusConditions(corev1.NodeCondition{
		Type:               corev1.NodeReady,
		Status:             readyStatus,
		LastTransitionTime: metav1.NewTime(clock.Now().Add(-transitionTimeOffset))}).Obj()
}

func TestNodeFailureReconciler(t *testing.T) {
	testStartTime := time.Now().Truncate(time.Second)
	nodeNameUnhealthy := "test-node-unhealthy"
	wlName := "test-workload"
	nsName := "default"
	fakeClock := testingclock.NewFakeClock(testStartTime)
	wlKey := types.NamespacedName{Name: wlName, Namespace: nsName}

	baseWorkload := utiltesting.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
		Admitted(true).
		Obj()

	basePod := testingpod.MakePod("test-pod", nsName).
		Annotation(kueuealpha.WorkloadAnnotation, wlName).
		Label(kueuealpha.TASLabel, "true").
		NodeName(nodeNameUnhealthy).
		Obj()

	tests := map[string]struct {
		initObjs       []client.Object
		req            reconcile.Request
		advanceClock   time.Duration
		wantFailedNode string
		wantRequeue    time.Duration
	}{
		"Node Found and Unhealthy (NotReady) - delay not passed": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionFalse, fakeClock, time.Duration(0)),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			req:            reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNode: "",
			wantRequeue:    NodeFailureDelay,
		},
		"Node Found and Unhealthy (NotReady) - delay passed": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionFalse, fakeClock, NodeFailureDelay),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			req:            reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNode: nodeNameUnhealthy,
		},
		"Node Found and Healthy": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionTrue, fakeClock, time.Duration(0)),
				utiltesting.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
				basePod.DeepCopy(),
			},
			req:            reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNode: "",
		},
		"Node Deleted": {
			initObjs: []client.Object{
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			req:            reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNode: nodeNameUnhealthy,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			fakeClock.SetTime(testStartTime)
			cl, r := setupTest(fakeClock, tc.initObjs...)
			result, err := r.Reconcile(ctx, tc.req)
			if err != nil {
				t.Errorf("Reconcile() error = %v", err)
			}
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, wlKey, wl); err != nil {
				t.Fatalf("Failed to get workload %q: %v", wlName, err)
			}

			gotFailedNode := wl.GetAnnotations()[kueuealpha.NodeToReplaceAnnotation]
			expectedNode := tc.wantFailedNode
			if diff := cmp.Diff(expectedNode, gotFailedNode); diff != "" {
				t.Errorf("Unexpected FailedNodes in annotation (-want/+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantRequeue, result.RequeueAfter); diff != "" {
				t.Errorf("Unexpected RequeueAfter (-want/+got):\n%s", diff)
			}
		})
	}
}
