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

package statefulset

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestEventHandlerPropagatesContext(t *testing.T) {
	type contextKey struct{}
	cases := map[string]struct {
		event            string
		cancelled        bool
		namespaceMatches bool
		wantLookups      int
		wantRequests     int
	}{
		"create in managed namespace":   {event: "create", namespaceMatches: true, wantLookups: 1, wantRequests: 1},
		"update in managed namespace":   {event: "update", namespaceMatches: true, wantLookups: 1, wantRequests: 1},
		"create in unmanaged namespace": {event: "create", wantLookups: 1},
		"update in unmanaged namespace": {event: "update", wantLookups: 1},
		"create with cancelled context": {event: "create", cancelled: true, namespaceMatches: true, wantLookups: 1},
		"update with cancelled context": {event: "update", cancelled: true, namespaceMatches: true, wantLookups: 1},
		"delete":                        {event: "delete", namespaceMatches: true, wantLookups: 1, wantRequests: 1},
		"generic is ignored":            {event: "generic", namespaceMatches: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var logs strings.Builder
			logger := funcr.New(func(_, line string) { logs.WriteString(line) }, funcr.Options{Verbosity: 5}).WithValues("trace", "caller-value")
			ctx := ctrl.LoggerInto(t.Context(), logger)
			ctx = context.WithValue(ctx, contextKey{}, "caller-value")
			deadline := time.Now().Add(time.Hour)
			ctx, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			lookups := 0
			cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(callCtx context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					lookups++
					if key.Name != "ns" {
						t.Errorf("Namespace lookup = %v, want ns", key)
					}
					if callCtx.Value(contextKey{}) != "caller-value" || callCtx.Done() != ctx.Done() {
						t.Error("Namespace lookup dropped the caller's values or cancellation")
					}
					if got, ok := callCtx.Deadline(); !ok || !got.Equal(deadline) {
						t.Errorf("Namespace lookup deadline = (%v, %v), want %v", got, ok, deadline)
					}
					if !errors.Is(callCtx.Err(), ctx.Err()) {
						t.Errorf("Namespace lookup context error = %v, want %v", callCtx.Err(), ctx.Err())
					}
					ctrl.LoggerFrom(callCtx).Info("Namespace lookup")
					if tc.namespaceMatches {
						obj.(*corev1.Namespace).Labels = map[string]string{"managed": "true"}
					}
					return callCtx.Err()
				},
			}).Build()
			h := statefulSetEventHandler{r: &Reconciler{
				integrationManager:           jobframework.NewIntegrationManager(),
				client:                       cl,
				manageJobsWithoutQueueName:   true,
				managedJobsNamespaceSelector: labels.SelectorFromSet(labels.Set{"managed": "true"}),
			}}
			obj := statefulset.MakeStatefulSet("job", "ns").Obj()
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			switch tc.event {
			case "create":
				h.Create(ctx, event.CreateEvent{Object: obj}, q)
			case "update":
				h.Update(ctx, event.UpdateEvent{ObjectOld: obj, ObjectNew: obj.DeepCopy()}, q)
			case "delete":
				h.Delete(ctx, event.DeleteEvent{Object: obj}, q)
			case "generic":
				h.Generic(ctx, event.GenericEvent{Object: obj}, q)
			}
			if lookups != tc.wantLookups {
				t.Errorf("Namespace lookups = %d, want %d", lookups, tc.wantLookups)
			}
			if tc.wantLookups > 0 && !strings.Contains(logs.String(), "caller-value") {
				t.Errorf("Namespace lookup dropped the caller's logger: %s", logs.String())
			}
			if tc.wantLookups > 0 && !strings.Contains(logs.String(), `"statefulSet"`) {
				t.Errorf("Event handler lost the established object log key: %s", logs.String())
			}
			if q.Len() != tc.wantRequests {
				t.Fatalf("Queued requests = %d, want %d", q.Len(), tc.wantRequests)
			}
			if tc.wantRequests > 0 {
				got, _ := q.Get()
				q.Done(got)
				if want := (reconcile.Request{NamespacedName: client.ObjectKeyFromObject(obj)}); got != want {
					t.Errorf("Request = %v, want %v", got, want)
				}
			}
		})
	}
}
