package tas

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func setupTest(initObjs ...client.Object) (client.Client, *nodeFailureReconciler) {
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
func setNodeConditionTest(t *testing.T, ctx context.Context, k8sClient client.Client, nodeName string, status corev1.ConditionStatus) {
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
				node.Status.Conditions[i].LastTransitionTime = metav1.Now()
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
		wantFailedNodes []string
	}{
		"Node Found and Unhealthy (NotReady)": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionFalse),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			req:             reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNodes: []string{nodeNameUnhealthy},
		},
		"Node Found and Unhealthy (Unknown)": {

			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionUnknown),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			req:             reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
			wantFailedNodes: []string{nodeNameUnhealthy},
		},
		"Node Recovers from Unhealthy": {
			initObjs: []client.Object{
				newNodeTest(nodeNameUnhealthy, corev1.ConditionTrue),
				utiltesting.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					NodesToReplace([]string{nodeNameUnhealthy}).
					Obj(),
				basePod.DeepCopy(),
			},
			req: reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeNameUnhealthy}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cl, r := setupTest(tt.initObjs...)

			_, err := r.Reconcile(ctx, tt.req)

			if err != nil {
				t.Errorf("Reconcile() error = %v", err)
			}
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, wlKey, wl); err != nil {
				t.Fatalf("Failed to get workload %q: %v", wlName, err)
			}
			if diff := cmp.Diff(tt.wantFailedNodes, wl.Status.NodesToReplace); diff != "" {
				t.Errorf("Unexpected FailedNodes (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestNodeFailureReconciler_Lifecycle(t *testing.T) {
	nodeName := "node"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Initial state: Healthy node, workload running
	cl, r := setupTest(
		newNodeTest(nodeName, corev1.ConditionTrue),
		baseWorkload.DeepCopy(),
		basePod.DeepCopy(),
	)

	// 2. Reconcile healthy node (no change expected)
	t.Log("Step 1: Reconciling healthy node")
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}})
	if err != nil {
		t.Fatalf("Reconcile healthy node failed: %v", err)
	}
	wl := &kueue.Workload{}
	if err := cl.Get(ctx, wlKey, wl); err != nil {
		t.Fatalf("Failed to get workload: %v", err)
	}
	if len(wl.Status.NodesToReplace) != 0 {
		t.Errorf("Expected FailedNodes to be empty, got %v", wl.Status.NodesToReplace)
	}

	// 3. Simulate Node becoming NotReady
	t.Log("Step 2: Simulating node becoming NotReady")
	setNodeConditionTest(t, ctx, cl, nodeName, corev1.ConditionFalse)

	// 4. Reconcile unhealthy node
	t.Log("Step 3: Reconciling unhealthy node")
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}})
	if err != nil {
		t.Fatalf("Reconcile unhealthy node failed: %v", err)
	}
	if err := cl.Get(ctx, wlKey, wl); err != nil {
		t.Fatalf("Failed to get workload: %v", err)
	}
	if !slices.Contains(wl.Status.NodesToReplace, nodeName) {
		t.Errorf("Expected node %q to be in FailedNodes, got %v", nodeName, wl.Status.NodesToReplace)
	}

	// 5. Simulate Node recovering
	t.Log("Step 4: Simulating node recovery")
	setNodeConditionTest(t, ctx, cl, nodeName, corev1.ConditionTrue)

	// 6. Reconcile recovered node
	t.Log("Step 5: Reconciling recovered node")
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}})
	if err != nil {
		t.Fatalf("Reconcile recovered node failed: %v", err)
	}
	if err := cl.Get(ctx, wlKey, wl); err != nil {
		t.Fatalf("Failed to get workload: %v", err)
	}
	if slices.Contains(wl.Status.NodesToReplace, nodeName) {
		t.Errorf("Expected node %q to be removed from FailedNodes, got %v", nodeName, wl.Status.NodesToReplace)
	}
	if len(wl.Status.NodesToReplace) != 0 {
		t.Errorf("Expected FailedNodes to be empty after recovery, got %v", wl.Status.NodesToReplace)
	}
}
