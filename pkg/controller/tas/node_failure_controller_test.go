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
	"slices"
	"strings"
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

// newNodeTest is a helper to create a Node object for testing.
func newNodeTest(name string, readyStatus corev1.ConditionStatus, conditions ...corev1.NodeCondition) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.Now(),
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Status:             readyStatus,
					LastTransitionTime: metav1.Now(),
					LastHeartbeatTime:  metav1.Now(),
					Reason:             "KubeletReady",
					Message:            "kubelet is ready.",
				},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeNetworkUnavailable, Status: corev1.ConditionFalse},
			},
		},
		Spec: corev1.NodeSpec{},
	}
	node.Status.Conditions = append(node.Status.Conditions, conditions...)
	return node
}

// setNodeConditionTest updates the Ready condition of a node in the fake client.
func setNodeConditionTest(ctx context.Context, t *testing.T, k8sClient client.Client, clock clock.Clock, nodeName string, status corev1.ConditionStatus) {
	t.Helper()
	node := &corev1.Node{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		t.Fatalf("Failed to get node %q: %v", nodeName, err)
	}

	updated := false
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			if node.Status.Conditions[i].Status != status {
				node.Status.Conditions[i].Status = status
				node.Status.Conditions[i].LastTransitionTime = metav1.NewTime(clock.Now())
				updated = true
			}
			break
		}
	}
	if !updated {
		t.Logf("Node %q already had Ready condition status %q", nodeName, status)
		return
	}

	if err := k8sClient.Status().Update(ctx, node); err != nil {
		t.Fatalf("Failed to update node %q status: %v", nodeName, err)
	}
	t.Logf("Successfully updated node %q Ready condition to %s", nodeName, status)
}

func TestNodeFailureReconciler(t *testing.T) {
	testStartTime := time.Now()
	nodeNameUnhealthy := "test-node-unhealthy"
	wlName := "test-workload"
	nsName := "default"
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
		initObjs        []client.Object
		req             reconcile.Request
		advanceClock    time.Duration // Time to advance clock before reconcile
		wantFailedNodes []string
		wantRequeue     time.Duration
	}{
		"Node Found and Unhealthy (NotReady) - delay not passed": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionFalse),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			req:             reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNodes: nil,
			wantRequeue:     NodeFailureDelay,
		},
		"Node Found and Unhealthy (NotReady) - delay passed": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionFalse),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			req:             reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			advanceClock:    NodeFailureDelay + time.Second,
			wantFailedNodes: []string{nodeNameUnhealthy},
		},
		"Node Found and Healthy": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionTrue),
				utiltesting.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
				basePod.DeepCopy(),
			},
			req:             reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNodes: nil,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeClock := testingclock.NewFakeClock(testStartTime)
			cl, r := setupTest(fakeClock, tt.initObjs...)

			if tt.advanceClock > 0 {
				fakeClock.Step(tt.advanceClock)
			}

			result, err := r.Reconcile(ctx, tt.req)
			if err != nil {
				t.Errorf("Reconcile() error = %v", err)
			}
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, wlKey, wl); err != nil {
				t.Fatalf("Failed to get workload %q: %v", wlName, err)
			}

			var actualFailedNodes []string
			annotations := wl.GetAnnotations()
			if ann, ok := annotations[kueuealpha.NodeToReplaceAnnotation]; ok && ann != "" {
				actualFailedNodes = strings.Split(ann, ",")
				slices.Sort(actualFailedNodes)
			}

			expectedNodes := tt.wantFailedNodes
			slices.Sort(expectedNodes)
			if diff := cmp.Diff(expectedNodes, actualFailedNodes); diff != "" {
				t.Errorf("Unexpected FailedNodes in annotation (-want/+got):\n%s", diff)
			}
			if (result.RequeueAfter - tt.wantRequeue).Abs() >= time.Second {
				diff := cmp.Diff(result.RequeueAfter, tt.wantRequeue)
				t.Errorf("Unexpected RequeueAfter (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestNodeFailureReconciler_Lifecycle(t *testing.T) {
	nodeName := "node"
	testStartTime := time.Now()
	podName := "pod"
	wlName := "workload"
	nsName := "default"
	wlKey := types.NamespacedName{Name: wlName, Namespace: nsName}

	baseWorkload := utiltesting.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
		Admitted(true).
		Obj()

	basePod := testingpod.MakePod(podName, nsName).
		Annotation(kueuealpha.WorkloadAnnotation, wlName).
		Label(kueuealpha.TASLabel, "true").
		NodeName(nodeName).
		Obj()

	ctx := t.Context()
	fakeClock := testingclock.NewFakeClock(testStartTime)
	cl, r := setupTest(fakeClock,
		newNodeTest(nodeName, corev1.ConditionTrue),
		baseWorkload.DeepCopy(),
		basePod.DeepCopy(),
	)

	t.Log("Step 1: Reconciling healthy node")
	result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}})
	if err != nil {
		t.Fatalf("Reconcile healthy node failed: %v", err)
	}
	wl := &kueue.Workload{}
	if err := cl.Get(ctx, wlKey, wl); err != nil {
		t.Fatalf("Failed to get workload: %v", err)
	}

	annotations := wl.GetAnnotations()
	if ann, ok := annotations[kueuealpha.NodeToReplaceAnnotation]; ok && ann != "" {
		actualFailedNodes := strings.Split(ann, ",")
		if len(actualFailedNodes) > 0 && (len(actualFailedNodes) > 1 || actualFailedNodes[0] != "") {
			t.Errorf("Expected FailedNodes annotation to be empty or not present, got %v", ann)
		}
	}
	if result.RequeueAfter != 0 {
		t.Errorf("Expected no requeue for healthy node, got %v", result.RequeueAfter)
	}

	t.Log("Step 2: Simulating node becoming NotReady")
	setNodeConditionTest(ctx, t, cl, fakeClock, nodeName, corev1.ConditionFalse)

	t.Log("Step 3: Reconciling unhealthy node")
	result, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}})
	if err != nil {
		t.Fatalf("Reconcile unhealthy node (before delay) failed: %v", err)
	}
	if err := cl.Get(ctx, wlKey, wl); err != nil {
		t.Fatalf("Failed to get workload: %v", err)
	}
	annotations = wl.GetAnnotations()
	if ann, ok := annotations[kueuealpha.NodeToReplaceAnnotation]; ok && ann != "" {
		actualFailedNodes := strings.Split(ann, ",")
		if len(actualFailedNodes) > 0 && (len(actualFailedNodes) > 1 || actualFailedNodes[0] != "") {
			t.Errorf("Expected FailedNodes annotation to be empty or not present before delay, got %v", ann)
		}
	}
	if result.RequeueAfter <= 0 {
		t.Errorf("Expected requeue after delay, got %v", result.RequeueAfter)
	}

	t.Log("Step 4: Advancing clock past delay")
	fakeClock.Step(NodeFailureDelay + time.Second)

	t.Log("Step 5: Reconciling unhealthy node after delay")
	result, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}})
	if err != nil {
		t.Fatalf("Reconcile unhealthy node failed: %v", err)
	}
	if err := cl.Get(ctx, wlKey, wl); err != nil {
		t.Fatalf("Failed to get workload: %v", err)
	}
	annotations = wl.GetAnnotations()
	annValue, found := annotations[kueuealpha.NodeToReplaceAnnotation]
	if !found {
		t.Fatalf("Expected annotation %q to be present", kueuealpha.NodeToReplaceAnnotation)
	}
	actualFailedNodes := strings.Split(annValue, ",")
	if !slices.Contains(actualFailedNodes, nodeName) {
		t.Errorf("Expected node %q to be in FailedNodes annotation %v, got %v", nodeName, kueuealpha.NodeToReplaceAnnotation, actualFailedNodes)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("Expected no requeue after marking failed, got %v", result.RequeueAfter)
	}
}
