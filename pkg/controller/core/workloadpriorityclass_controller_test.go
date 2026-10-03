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
	"context"
	"errors"
	"fmt"
	"maps"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestWorkloadPriorityClassPredicates(t *testing.T) {
	cases := map[string]struct {
		eventType string
		oldWPC    *kueue.WorkloadPriorityClass
		newWPC    *kueue.WorkloadPriorityClass
		want      bool
	}{
		"create event should trigger reconcile": {
			eventType: "create",
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      true,
		},
		"delete event should not trigger reconcile": {
			eventType: "delete",
			oldWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      false,
		},
		"update event with changed priority should trigger reconcile": {
			eventType: "update",
			oldWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(200).Obj(),
			want:      true,
		},
		"update event with unchanged priority should not trigger reconcile": {
			eventType: "update",
			oldWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      false,
		},
		"generic event should not trigger reconcile": {
			eventType: "generic",
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reconciler := NewWorkloadPriorityClassReconciler(nil, nil, nil)
			var got bool

			switch tc.eventType {
			case "create":
				got = reconciler.Create(event.TypedCreateEvent[*kueue.WorkloadPriorityClass]{Object: tc.newWPC})
			case "delete":
				got = reconciler.Delete(event.TypedDeleteEvent[*kueue.WorkloadPriorityClass]{Object: tc.oldWPC})
			case "update":
				got = reconciler.Update(event.TypedUpdateEvent[*kueue.WorkloadPriorityClass]{
					ObjectOld: tc.oldWPC,
					ObjectNew: tc.newWPC,
				})
			case "generic":
				got = reconciler.Generic(event.TypedGenericEvent[*kueue.WorkloadPriorityClass]{Object: tc.newWPC})
			}

			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkloadPriorityClassReconcile(t *testing.T) {
	errTest := errors.New("test error")
	cases := map[string]struct {
		wpc           *kueue.WorkloadPriorityClass
		workloads     []kueue.Workload
		wantWorkloads []kueue.Workload
		wantError     error
		clientFuncs   *interceptor.Funcs
		// lastRun is what the reconciler recorded before this run.
		lastRun map[string]classRevision
		// apiServer is what the API server holds when it differs from the
		// cache (an empty list is a server without the workload); writes then
		// land there and wantWorkloads is read from there.
		apiServer []kueue.Workload
		// liveReadError fails the reads of Workloads from the API server.
		liveReadError error
		// failWriteOf fails the writes of the Workload of this name to the
		// API server, so it needs apiServer.
		failWriteOf string
		// wantLiveReads is how many Workloads the run read from the API server.
		wantLiveReads int32
		// wantLastRun, when set, is what the reconciler recorded after this run.
		wantLastRun map[string]classRevision
	}{
		"reconcile updates workload priority when WPC priority changes": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile updates multiple workloads": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile updates local workload and skips MultiKueue remote workload": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("local", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("remote", "default").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("local", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("remote", "default").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile skips workloads with up-to-date priority": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile succeeds when no workloads use the WPC": {
			wpc:           utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads:     []kueue.Workload{},
			wantWorkloads: []kueue.Workload{},
		},
		"reconcile handles workload not found error": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			clientFuncs: &interceptor.Funcs{
				Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					return apierrors.NewNotFound(kueue.Resource("workload"), "wl1")
				},
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile returns error when update fails": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			clientFuncs: &interceptor.Funcs{
				Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					return errTest
				},
			},
			wantError: errTest,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile handles partial update failures": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			clientFuncs: &interceptor.Funcs{
				Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					wl := obj.(*kueue.Workload)
					if wl.Name == "wl2" {
						return errTest
					}
					return client.Update(ctx, obj, opts...)
				},
			},
			wantError: errTest,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile returns an error when all updates fail": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl3", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			clientFuncs: &interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					return fmt.Errorf("%w: update failed for %s", errTest, obj.GetName())
				},
			},
			wantError: errTest,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl3", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile handles WPC not found": {
			wpc:           utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads:     []kueue.Workload{},
			wantWorkloads: []kueue.Workload{},
			clientFuncs: &interceptor.Funcs{
				Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return apierrors.NewNotFound(kueue.Resource("workloadpriorityclass"), key.Name)
				},
			},
		},
		"reconcile trusts the cache when the class is unchanged since the last run": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(1).PriorityValue(1000).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
		},
		"reconcile repairs from the API server a class edited back before the cache saw the earlier write": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(50).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				// The earlier run's write moved wl1 on the API server past the cache.
				*utiltestingapi.MakeWorkload("wl1", "default").
					ResourceVersion("1000").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(50).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile reads live after an edit that kept the class value, and writes nothing": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(1000).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			failWriteOf:   "wl1",
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile confirms with the API server after the class was re-created": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("recreated").Generation(1).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				// The earlier run's write moved wl1 on the API server past the cache.
				*utiltestingapi.MakeWorkload("wl1", "default").
					ResourceVersion("1000").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "recreated", generation: 1}},
		},
		"reconcile skips a MultiKueue remote workload before asking the API server": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("remote", "default").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("remote", "default").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("remote", "default").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile leaves alone a workload the API server holds as MultiKueue remote under a cached local one": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					UID("local").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					UID("remote").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Priority(700).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					UID("remote").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Priority(700).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile leaves alone a workload the API server moved to another class": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(50).
					WorkloadPriorityClassRef("low").
					Obj(),
			},
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(50).
					WorkloadPriorityClassRef("low").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile leaves alone a workload the API server moved to a pod priority class of the same name": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(50).
					PodPriorityClassRef("high").
					Obj(),
			},
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(50).
					PodPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile leaves alone a live workload whose MultiKueue origin label is empty": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Label(kueue.MultiKueueOriginLabel, "").
					Priority(700).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Label(kueue.MultiKueueOriginLabel, "").
					Priority(700).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile skips a workload the API server no longer has": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer:     []kueue.Workload{},
			wantLiveReads: 1,
			wantWorkloads: []kueue.Workload{},
			wantLastRun:   map[string]classRevision{"high": {uid: "high", generation: 2}},
		},
		"reconcile keeps the last record when a read from the API server fails": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			liveReadError: errTest,
			wantLiveReads: 1,
			wantError:     errTest,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
		},
		"reconcile keeps the last record when a write fails": {
			wpc:     utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(2).PriorityValue(100).Obj(),
			lastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			failWriteOf:   "wl1",
			wantLiveReads: 1,
			wantError:     errTest,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
		},
		"reconcile records a class at first sight even when a write fails": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").UID("high").Generation(1).PriorityValue(200).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			apiServer: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			failWriteOf: "wl2",
			wantError:   errTest,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantLastRun: map[string]classRevision{"high": {uid: "high", generation: 1}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()

			builder := utiltesting.NewClientBuilder().
				WithObjects(tc.wpc).
				WithIndex(&kueue.Workload{}, indexer.WorkloadPriorityClassKey, indexer.IndexWorkloadPriorityClass).
				WithStatusSubresource(&kueue.Workload{})
			for i := range tc.workloads {
				builder = builder.WithObjects(&tc.workloads[i])
			}
			// The API server behind the cache. The reconciler reads Workloads
			// from it live and, when apiServer is set, its writes land there.
			stored := tc.workloads
			if tc.apiServer != nil {
				stored = tc.apiServer
			}
			serverBuilder := utiltesting.NewClientBuilder().WithStatusSubresource(&kueue.Workload{})
			for i := range stored {
				serverBuilder = serverBuilder.WithObjects(&stored[i])
			}
			server := serverBuilder.Build()
			var liveReads atomic.Int32
			liveServer := interceptor.NewClient(server, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, isWorkload := obj.(*kueue.Workload); isWorkload {
						liveReads.Add(1)
						if tc.liveReadError != nil {
							return tc.liveReadError
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if obj.GetName() == tc.failWriteOf {
						return errTest
					}
					return c.Update(ctx, obj, opts...)
				},
			})
			cacheFuncs := interceptor.Funcs{}
			if tc.clientFuncs != nil {
				cacheFuncs = *tc.clientFuncs
			}
			if tc.apiServer != nil {
				cacheFuncs.Update = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if _, isWorkload := obj.(*kueue.Workload); isWorkload {
						return liveServer.Update(ctx, obj, opts...)
					}
					return c.Update(ctx, obj, opts...)
				}
			}
			k8sClient := builder.WithInterceptorFuncs(cacheFuncs).Build()
			var stateStore client.Client = k8sClient
			if tc.apiServer != nil {
				stateStore = server
			}

			reconciler := NewWorkloadPriorityClassReconciler(k8sClient, liveServer, nil)
			maps.Copy(reconciler.lastRun, tc.lastRun)
			req := reconcile.Request{
				Name: tc.wpc.Name,
			}

			_, gotErr := reconciler.Reconcile(ctx, req)
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want/+got):\n%s", diff)
			}
			if gotLiveReads := liveReads.Load(); gotLiveReads != tc.wantLiveReads {
				t.Errorf("Reconcile read %d workloads from the API server, want %d", gotLiveReads, tc.wantLiveReads)
			}
			if tc.wantLastRun != nil {
				if diff := cmp.Diff(tc.wantLastRun, reconciler.lastRun, cmp.AllowUnexported(classRevision{})); diff != "" {
					t.Errorf("Unexpected record of the class (-want/+got):\n%s", diff)
				}
			}
			// Verify workloads are in the expected state
			for _, wantWl := range tc.wantWorkloads {
				gotWl := &kueue.Workload{}
				err := stateStore.Get(ctx, types.NamespacedName{Name: wantWl.Name, Namespace: wantWl.Namespace}, gotWl)
				if err != nil {
					t.Fatalf("failed to get workload %s: %v", wantWl.Name, err)
				}
				if diff := cmp.Diff(wantWl, *gotWl, workloadCmpOpts...); diff != "" {
					t.Errorf("workload %s mismatch (-want +got):\n%s", wantWl.Name, diff)
				}
			}
		})
	}
}
