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

package admissioncheck

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestGetSchedulableRemoteClusters(t *testing.T) {
	getErr := errors.New("cannot read cluster")
	cases := map[string]struct {
		gate     bool
		cordoned []string
		missing  bool
		getErr   error
		want     []string
	}{
		"preserves configuration order": {gate: true, want: []string{"z", "a"}},
		"skips cordoned cluster":        {gate: true, cordoned: []string{"z"}, want: []string{"a"}},
		"all cordoned":                  {gate: true, cordoned: []string{"z", "a"}},
		"skips missing cluster":         {gate: true, missing: true, want: []string{"a"}},
		"disabled gate":                 {cordoned: []string{"z", "a"}, want: []string{"z", "a"}},
		"read error":                    {gate: true, getErr: getErr},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueClusterCordon, tc.gate)
			ctx, _ := utiltesting.ContextWithLog(t)
			a := utiltestingapi.MakeMultiKueueCluster("a").Obj()
			z := utiltestingapi.MakeMultiKueueCluster("z").Obj()
			for _, name := range tc.cordoned {
				if name == "a" {
					a.Spec.Unschedulable = new(true)
				} else {
					z.Spec.Unschedulable = new(true)
				}
			}
			builder := utiltesting.NewClientBuilder().WithObjects(a,
				utiltestingapi.MakeMultiKueueConfig("cfg").Clusters("z", "a").Obj(),
				utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "cfg").Obj()).
				WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*kueue.MultiKueueCluster); ok && tc.getErr != nil {
						return tc.getErr
					}
					return c.Get(ctx, key, obj, opts...)
				}})
			if !tc.missing {
				builder = builder.WithObjects(z)
			}
			helper, err := NewMultiKueueStoreHelper(builder.Build())
			if err != nil {
				t.Fatal(err)
			}
			got, err := GetSchedulableRemoteClusters(ctx, helper, "ac")
			if diff := cmp.Diff(tc.getErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error: %s", diff)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected clusters: %s", diff)
			}
		})
	}
}

func TestMultiKueueClusterCordonHandler(t *testing.T) {
	cases := map[string]struct {
		gate, old, next bool
		want            int
	}{
		"cordon queues related workloads":   {gate: true, next: true, want: 1},
		"uncordon queues related workloads": {gate: true, old: true, want: 1},
		"unchanged policy is ignored":       {gate: true, old: true, next: true},
		"disabled feature is ignored":       {next: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueClusterCordon, tc.gate)
			ctx, _ := utiltesting.ContextWithLog(t)
			c := utiltesting.NewFakeClient(
				utiltestingapi.MakeMultiKueueConfig("cfg").Clusters("worker").Obj(),
				utiltestingapi.MakeMultiKueueConfig("other").Clusters("elsewhere").Obj(),
				utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "cfg").Obj(),
				utiltestingapi.MakeAdmissionCheck("other").ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "other").Obj(),
				utiltestingapi.MakeWorkload("wl", "ns").AdmissionCheck(kueue.AdmissionCheckState{Name: "ac", State: kueue.CheckStatePending}).Obj(),
				utiltestingapi.MakeWorkload("unrelated", "ns").AdmissionCheck(kueue.AdmissionCheckState{Name: "other", State: kueue.CheckStatePending}).Obj(),
			)
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			h := NewMultiKueueClusterHandler(c, 0)
			h.Update(ctx, event.UpdateEvent{
				ObjectOld: utiltestingapi.MakeMultiKueueCluster("worker").Unschedulable(tc.old).Obj(),
				ObjectNew: utiltestingapi.MakeMultiKueueCluster("worker").Unschedulable(tc.next).Obj(),
			}, q)
			if q.Len() != tc.want {
				t.Fatalf("queued %d workloads, want %d", q.Len(), tc.want)
			}
			if tc.want > 0 {
				got, _ := q.Get()
				q.Done(got)
				want := reconcile.Request{Name: "wl", Namespace: "ns"}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("wrong workload queued: %s", diff)
				}
			}
		})
	}
}
