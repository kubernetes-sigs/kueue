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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestTopologyReconcilerFinalizerRemovalOnDeletion(t *testing.T) {
	cases := map[string]struct {
		flavorInCache     bool
		wantRequeueAfter  time.Duration
		wantFinalizerGone bool
	}{
		"topology still referenced in the TAS cache is requeued": {
			flavorInCache:    true,
			wantRequeueAfter: topologyInUseRequeueInterval,
		},
		"topology with no referencing flavors gets the finalizer removed": {
			wantFinalizerGone: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)

			now := metav1.Now()
			topology := utiltestingapi.MakeTopology("t1").Levels("kubernetes.io/hostname").Obj()
			topology.Finalizers = []string{kueue.ResourceInUseFinalizerName}
			topology.DeletionTimestamp = &now

			cl := utiltesting.NewFakeClient(topology)
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateTopology(log, topology)
			if tc.flavorInCache {
				flavor := utiltestingapi.MakeResourceFlavor("rf").TopologyName("t1").Obj()
				cqCache.AddOrUpdateResourceFlavor(log, flavor)
			}

			r := newTopologyReconciler(cl, nil, cqCache, nil)
			got, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "t1"}})
			if err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}
			if got.RequeueAfter != tc.wantRequeueAfter {
				t.Errorf("unexpected RequeueAfter: want %v, got %v", tc.wantRequeueAfter, got.RequeueAfter)
			}

			gotTopology := &kueue.Topology{}
			getErr := cl.Get(ctx, types.NamespacedName{Name: "t1"}, gotTopology)
			if tc.wantFinalizerGone {
				// The fake client deletes the object once the last finalizer
				// is removed from a terminating object.
				if getErr == nil && len(gotTopology.Finalizers) != 0 {
					t.Errorf("expected finalizer to be removed, still present: %v", gotTopology.Finalizers)
				}
				if getErr != nil && !apierrors.IsNotFound(getErr) {
					t.Errorf("unexpected error getting topology: %v", getErr)
				}
			} else {
				if getErr != nil {
					t.Fatalf("unexpected error getting topology: %v", getErr)
				}
				if len(gotTopology.Finalizers) == 0 {
					t.Error("expected finalizer to be kept while the topology is in use")
				}
			}
		})
	}
}
