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

package pod

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// TestReconcilePodQueueAndPriority checks combined updates for standalone Pods
// and Pod groups while preserving serving groups' existing queues.
func TestReconcilePodQueueAndPriority(t *testing.T) {
	cases := map[string]struct {
		group     bool
		serving   bool
		wantQueue kueue.LocalQueueName
	}{
		"standalone pod changes queue with priority": {wantQueue: "new-queue"},
		"pod group changes queue with priority":      {group: true, wantQueue: "new-queue"},
		"serving pod group keeps its queue":          {group: true, serving: true, wantQueue: "old-queue"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			})
			ctx, _ := utiltesting.ContextWithLog(t)
			podWrapper := testingpod.MakePod("pod", "ns").UID("pod-uid").
				Queue("new-queue").WorkloadPriorityClass("high").
				ManagedByKueueLabel().KueueFinalizer().KueueSchedulingGate()
			if tc.group {
				podWrapper.GroupNameLabel("group").GroupTotalCount("1").RoleHash("role")
			}
			if tc.serving {
				podWrapper.PodGroupServingAnnotation()
			}
			obj := podWrapper.Obj()
			p := FromObject(obj)
			p.isGroup = tc.group
			p.list.Items = []corev1.Pod{*obj}
			podSets, err := p.PodSets(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			key := types.NamespacedName{Name: obj.Name, Namespace: obj.Namespace}
			wlWrapper := utiltestingapi.MakeWorkload(GetWorkloadNameForPod(obj.Name, obj.UID), obj.Namespace).
				Finalizers(kueue.ResourceInUseFinalizerName).PodSets(podSets...).
				Queue("old-queue").WorkloadPriorityClassRef("low").Priority(10)
			if tc.group {
				wlWrapper.Name("group").Group().OwnerReference(p.GVK(), obj.Name, string(obj.UID))
				key = types.NamespacedName{Name: "group", Namespace: "group/ns"}
			} else {
				wlWrapper.ControllerReference(p.GVK(), obj.Name, string(obj.UID)).
					Label(controllerconstants.JobUIDLabel, string(obj.UID))
			}
			wl := wlWrapper.Obj()
			var writes []kueue.WorkloadSpec
			builder := utiltesting.NewClientBuilder().
				WithObjects(utiltesting.MakeNamespace(obj.Namespace), obj, wl,
					utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj()).
				WithStatusSubresource(wl).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if err := c.Update(ctx, obj, opts...); err != nil {
							return err
						}
						if updated, ok := obj.(*kueue.Workload); ok {
							writes = append(writes, *updated.Spec.DeepCopy())
						}
						return nil
					},
				})
			indexer := utiltesting.AsIndexer(builder)
			if err := SetupIndexes(ctx, indexer); err != nil {
				t.Fatal(err)
			}
			cl := builder.Build()
			rec, err := NewReconciler(ctx, cl, indexer, &utiltesting.EventRecorder{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rec.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			want := wl.Spec.DeepCopy()
			want.QueueName = tc.wantQueue
			want.PriorityClassRef.Name = "high"
			want.Priority = new(int32(100))
			if len(writes) != 1 {
				t.Errorf("workload writes = %d, want 1", len(writes))
			}
			for i := range writes {
				if diff := cmp.Diff(*want, writes[i], cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("write %d (-want,+got):\n%s", i, diff)
				}
			}
			persisted := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), persisted); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(*want, persisted.Spec, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("persisted workload (-want,+got):\n%s", diff)
			}
		})
	}
}
