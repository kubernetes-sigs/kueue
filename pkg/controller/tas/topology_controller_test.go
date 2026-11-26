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
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestTopologyResourceFlavorHandler(t *testing.T) {
	cases := map[string]struct {
		event       any
		wantRequeue []reconcile.Request
	}{
		"update unsets topologyName": {
			event: event.UpdateEvent{
				ObjectOld: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
				ObjectNew: utiltestingapi.MakeResourceFlavor("rf").Obj(),
			},
			wantRequeue: []reconcile.Request{{Name: "topo-a"}},
		},
		"update changes topologyName": {
			event: event.UpdateEvent{
				ObjectOld: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
				ObjectNew: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-b").Obj(),
			},
			wantRequeue: []reconcile.Request{{Name: "topo-a"}},
		},
		"update sets topologyName from nil": {
			event: event.UpdateEvent{
				ObjectOld: utiltestingapi.MakeResourceFlavor("rf").Obj(),
				ObjectNew: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
			},
		},
		"update keeps topologyName unchanged": {
			event: event.UpdateEvent{
				ObjectOld: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").NodeLabel("k", "v1").Obj(),
				ObjectNew: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").NodeLabel("k", "v2").Obj(),
			},
		},
		"update with non-ResourceFlavor object is ignored": {
			event: event.UpdateEvent{
				ObjectOld: &corev1.Node{},
				ObjectNew: &corev1.Node{},
			},
		},
		"delete with topologyName enqueues topology": {
			event: event.DeleteEvent{
				Object: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
			},
			wantRequeue: []reconcile.Request{{Name: "topo-a"}},
		},
		"delete without topologyName enqueues nothing": {
			event: event.DeleteEvent{
				Object: utiltestingapi.MakeResourceFlavor("rf").Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			h := &resourceFlavorHandler{}
			q := &utiltesting.MockTypedRateLimitingInterface{}

			switch e := tc.event.(type) {
			case event.UpdateEvent:
				h.Update(ctx, e, q)
			case event.DeleteEvent:
				h.Delete(ctx, e, q)
			}

			if diff := cmp.Diff(tc.wantRequeue, q.Items, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected requeued Topologies (-want +got):\n%s", diff)
			}
		})
	}
}
