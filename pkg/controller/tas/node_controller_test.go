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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/internal/mocks/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestReconciler(t *testing.T) {
	const nodeName = "test-node"

	testCases := map[string]struct {
		req          ctrl.Request
		nodes        []corev1.Node
		addedNodes   []string
		deletedNodes []string
	}{
		"create node": {
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Name: nodeName}},
			nodes:      []corev1.Node{*node.MakeNode(nodeName).Ready().Obj()},
			addedNodes: []string{nodeName},
		},
		"update node": {
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Name: nodeName}},
			nodes:      []corev1.Node{*node.MakeNode(nodeName).Ready().Obj()},
			addedNodes: []string{nodeName},
		},
		"update node (unschedulable)": {
			req:          ctrl.Request{NamespacedName: types.NamespacedName{Name: nodeName}},
			nodes:        []corev1.Node{*node.MakeNode(nodeName).Unschedulable().Obj()},
			deletedNodes: []string{nodeName},
		},
		"update node (not ready)": {
			req:          ctrl.Request{NamespacedName: types.NamespacedName{Name: nodeName}},
			nodes:        []corev1.Node{*node.MakeNode(nodeName).Unschedulable().Obj()},
			deletedNodes: []string{nodeName},
		},
		"delete node": {
			req:          ctrl.Request{NamespacedName: types.NamespacedName{Name: nodeName}},
			deletedNodes: []string{nodeName},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder()
			for i := range tc.nodes {
				clientBuilder = clientBuilder.WithObjects(&tc.nodes[i])
			}

			kClient := clientBuilder.Build()

			addedNodes := make([]string, 0)
			deletedNodes := make([]string, 0)

			mockctrl := gomock.NewController(t)
			mTASCache := mocks.NewMockNodeTASCache(mockctrl)
			mTASCache.EXPECT().AddOrUpdateNode(gomock.Any()).Do(func(node *corev1.Node) {
				addedNodes = append(addedNodes, node.Name)
			}).AnyTimes()
			mTASCache.EXPECT().DeleteNode(gomock.Any()).Do(func(nodeName string) {
				deletedNodes = append(deletedNodes, nodeName)
			}).AnyTimes()

			_, err := newNodeReconciler(kClient, mTASCache, nil).Reconcile(ctx, tc.req)
			if err != nil {
				t.Errorf("Reconcile() error = %v", err)
			}

			if diff := cmp.Diff(tc.addedNodes, addedNodes, cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("unexpected added nodes (-want +got) %s", diff)
			}

			if diff := cmp.Diff(tc.deletedNodes, deletedNodes, cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("unexpected deleted nodes (-want +got) %s", diff)
			}
		})
	}
}
