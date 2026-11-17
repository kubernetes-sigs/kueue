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

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"kueueviz/middleware"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func makeWorkloadObj(namespace, name, queueName string, conditions ...metav1.Condition) *kueueapi.Workload {
	return &kueueapi.Workload{
		Namespace: namespace, Name: name,
		Spec:   kueueapi.WorkloadSpec{QueueName: kueueapi.LocalQueueName(queueName)},
		Status: kueueapi.WorkloadStatus{Conditions: conditions},
	}
}

func TestFetchWorkloads(t *testing.T) {
	objs := []ctrlclient.Object{
		makeWorkloadObj("ns-1", "wl-1", "lq-1"),
		makeWorkloadObj("ns-2", "wl-2", "lq-1"),
	}

	tests := map[string]struct {
		funcs     *interceptor.Funcs
		namespace string
		want      []string
		wantErr   bool
	}{
		"empty namespace lists all workloads": {
			want: []string{"ns-1/wl-1", "ns-2/wl-2"},
		},
		"namespace filters workloads": {
			namespace: "ns-2",
			want:      []string{"ns-2/wl-2"},
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, objs...), nil, nil)

			got, err := h.fetchWorkloads(t.Context(), tc.namespace, middleware.Identity{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchWorkloads() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			var gotNames []string
			for _, wl := range got.(*kueueapi.WorkloadList).Items {
				gotNames = append(gotNames, wl.Namespace+"/"+wl.Name)
			}
			if diff := cmp.Diff(tc.want, gotNames); diff != "" {
				t.Errorf("unexpected workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFetchWorkloadDetails(t *testing.T) {
	preempted := metav1.Condition{
		Type:   kueueapi.WorkloadPreempted,
		Status: metav1.ConditionTrue,
		Reason: "InClusterQueue",
	}
	admittedWorkload := makeWorkloadObj("ns-1", "wl-1", "lq-1")
	admittedWorkload.Status.Admission = &kueueapi.Admission{ClusterQueue: "cq-admitted"}
	workloadWithoutQueue := admittedWorkload.DeepCopy()
	workloadWithoutQueue.Spec.QueueName = ""
	workloadWithoutAdmissionClusterQueue := admittedWorkload.DeepCopy()
	workloadWithoutAdmissionClusterQueue.Status.Admission.ClusterQueue = ""

	tests := map[string]struct {
		objs                 []ctrlclient.Object
		funcs                *interceptor.Funcs
		authorizer           middleware.Authorizer
		workloadName         string
		wantClusterQueueName string
		wantPreemption       map[string]any
		wantAuthCalls        int
		wantLocalQueueGets   int
		wantErr              bool
	}{
		"cluster queue name comes from the local queue": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1"),
				makeLocalQueue("ns-1", "lq-1", "cq-a"),
			},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-a",
			wantPreemption:       map[string]any{},
			wantLocalQueueGets:   1,
		},
		"preemption condition is reported": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1", preempted),
				makeLocalQueue("ns-1", "lq-1", "cq-a"),
			},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-a",
			wantPreemption:       map[string]any{"preempted": true, "reason": "InClusterQueue"},
			wantLocalQueueGets:   1,
		},
		"authorized local queue access returns its cluster queue": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1"),
				makeLocalQueue("ns-1", "lq-1", "cq-a"),
			},
			authorizer:           stubAuthorizer{allowed: true},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-a",
			wantPreemption:       map[string]any{},
			wantAuthCalls:        1,
			wantLocalQueueGets:   1,
		},
		"admission takes precedence over the local queue": {
			objs: []ctrlclient.Object{
				admittedWorkload.DeepCopy(),
				makeLocalQueue("ns-1", "lq-1", "cq-current"),
			},
			authorizer:           stubAuthorizer{allowed: true},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-admitted",
			wantPreemption:       map[string]any{},
		},
		"empty admission cluster queue falls back to the local queue": {
			objs: []ctrlclient.Object{
				workloadWithoutAdmissionClusterQueue.DeepCopy(),
				makeLocalQueue("ns-1", "lq-1", "cq-current"),
			},
			authorizer:           stubAuthorizer{allowed: true},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-current",
			wantPreemption:       map[string]any{},
			wantAuthCalls:        1,
			wantLocalQueueGets:   1,
		},
		"denied local queue access does not disclose its cluster queue": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1"),
				makeLocalQueue("ns-1", "lq-1", "cq-hidden"),
			},
			authorizer:           stubAuthorizer{allowed: false},
			workloadName:         "wl-1",
			wantClusterQueueName: "Unknown",
			wantPreemption:       map[string]any{},
			wantAuthCalls:        1,
		},
		"admission skips local queue authorization when access would be denied": {
			objs: []ctrlclient.Object{
				admittedWorkload.DeepCopy(),
				makeLocalQueue("ns-1", "lq-1", "cq-hidden"),
			},
			authorizer:           stubAuthorizer{allowed: false},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-admitted",
			wantPreemption:       map[string]any{},
		},
		"local queue authorization error does not disclose its cluster queue": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1"),
				makeLocalQueue("ns-1", "lq-1", "cq-hidden"),
			},
			authorizer:           stubAuthorizer{allowed: true, err: errors.New("sar failed")},
			workloadName:         "wl-1",
			wantClusterQueueName: "Unknown",
			wantPreemption:       map[string]any{},
			wantAuthCalls:        1,
		},
		"admission skips local queue authorization when it would fail": {
			objs: []ctrlclient.Object{
				admittedWorkload.DeepCopy(),
				makeLocalQueue("ns-1", "lq-1", "cq-hidden"),
			},
			authorizer:           stubAuthorizer{allowed: false, err: errors.New("sar failed")},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-admitted",
			wantPreemption:       map[string]any{},
		},
		"workload without queue name has unknown cluster queue": {
			objs:                 []ctrlclient.Object{makeWorkloadObj("ns-1", "wl-1", "")},
			workloadName:         "wl-1",
			wantClusterQueueName: "Unknown",
			wantPreemption:       map[string]any{},
		},
		"workload without queue name uses the admission": {
			objs:                 []ctrlclient.Object{workloadWithoutQueue.DeepCopy()},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-admitted",
			wantPreemption:       map[string]any{},
		},
		"local queue without cluster queue has unknown cluster queue": {
			objs: []ctrlclient.Object{
				makeWorkloadObj("ns-1", "wl-1", "lq-1"),
				makeLocalQueue("ns-1", "lq-1", ""),
			},
			workloadName:         "wl-1",
			wantClusterQueueName: "Unknown",
			wantPreemption:       map[string]any{},
			wantLocalQueueGets:   1,
		},
		"admission is used when the local queue has no cluster queue": {
			objs: []ctrlclient.Object{
				admittedWorkload.DeepCopy(),
				makeLocalQueue("ns-1", "lq-1", ""),
			},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-admitted",
			wantPreemption:       map[string]any{},
		},
		"missing local queue has unknown cluster queue": {
			objs:                 []ctrlclient.Object{makeWorkloadObj("ns-1", "wl-1", "lq-1")},
			workloadName:         "wl-1",
			wantClusterQueueName: "Unknown",
			wantPreemption:       map[string]any{},
			wantLocalQueueGets:   1,
		},
		"admission is used when the local queue is missing": {
			objs: []ctrlclient.Object{func() ctrlclient.Object {
				wl := makeWorkloadObj("ns-1", "wl-1", "lq-1")
				wl.Status.Admission = &kueueapi.Admission{ClusterQueue: "cq-a"}
				return wl
			}()},
			workloadName:         "wl-1",
			wantClusterQueueName: "cq-a",
			wantPreemption:       map[string]any{},
		},
		"local queue get error is returned": {
			objs: []ctrlclient.Object{makeWorkloadObj("ns-1", "wl-1", "lq-1")},
			funcs: &interceptor.Funcs{
				Get: func(ctx context.Context, c ctrlclient.WithWatch, key ctrlclient.ObjectKey, obj ctrlclient.Object, opts ...ctrlclient.GetOption) error {
					if _, ok := obj.(*kueueapi.LocalQueue); ok {
						return errors.New("boom")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			},
			workloadName:       "wl-1",
			wantLocalQueueGets: 1,
			wantErr:            true,
		},
		"missing workload returns error": {
			workloadName: "missing",
			wantErr:      true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var authCalls []authorizationv1.ResourceAttributes
			var authorizer middleware.Authorizer
			if tc.authorizer != nil {
				authorizer = authorizerFunc(func(attrs authorizationv1.ResourceAttributes) (bool, error) {
					authCalls = append(authCalls, attrs)
					return tc.authorizer.Authorize(t.Context(), middleware.Identity{}, attrs)
				})
			}
			localQueueGets := 0
			funcs := &interceptor.Funcs{
				Get: func(ctx context.Context, c ctrlclient.WithWatch, key ctrlclient.ObjectKey, obj ctrlclient.Object, opts ...ctrlclient.GetOption) error {
					if _, ok := obj.(*kueueapi.LocalQueue); ok {
						localQueueGets++
					}
					if tc.funcs != nil && tc.funcs.Get != nil {
						return tc.funcs.Get(ctx, c, key, obj, opts...)
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}
			h := New(newFakeClient(t, funcs, tc.objs...), nil, authorizer)

			got, err := h.fetchWorkloadDetails(t.Context(), "ns-1", tc.workloadName, middleware.Identity{Username: "alice"})
			if len(authCalls) != tc.wantAuthCalls {
				t.Errorf("authorization calls = %d, want %d", len(authCalls), tc.wantAuthCalls)
			}
			for _, attrs := range authCalls {
				want := middleware.ResourceAccess("get", LocalQueuesGVR(), "ns-1", "lq-1")
				if diff := cmp.Diff(want, attrs); diff != "" {
					t.Errorf("unexpected authorization request (-want,+got):\n%s", diff)
				}
			}
			if localQueueGets != tc.wantLocalQueueGets {
				t.Errorf("local queue gets = %d, want %d", localQueueGets, tc.wantLocalQueueGets)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchWorkloadDetails() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			// Check the JSON the frontend receives: the workload object
			// plus the clusterQueueName and preemption fields.
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var body struct {
				Metadata         metav1.ObjectMeta `json:"metadata"`
				ClusterQueueName string            `json:"clusterQueueName"`
				Preemption       map[string]any    `json:"preemption"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if body.Metadata.Name != tc.workloadName {
				t.Errorf("metadata.name = %q, want %q", body.Metadata.Name, tc.workloadName)
			}
			if body.ClusterQueueName != tc.wantClusterQueueName {
				t.Errorf("clusterQueueName = %q, want %q", body.ClusterQueueName, tc.wantClusterQueueName)
			}
			if diff := cmp.Diff(tc.wantPreemption, body.Preemption); diff != "" {
				t.Errorf("unexpected preemption (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestWorkloadDetailsWebSocketAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	identity := middleware.Identity{
		Username: "alice",
		UID:      "alice-uid",
		Groups:   []string{"team-a", "system:authenticated"},
		Extra:    map[string][]string{"scope": {"workload-viewer"}},
	}
	var authMu sync.Mutex
	var authCalls []authorizationv1.ResourceAttributes
	authorizer := identityAuthorizerFunc(func(got middleware.Identity, attrs authorizationv1.ResourceAttributes) (bool, error) {
		if diff := cmp.Diff(identity, got); diff != "" {
			t.Errorf("unexpected authorization identity (-want,+got):\n%s", diff)
		}
		authMu.Lock()
		authCalls = append(authCalls, attrs)
		authMu.Unlock()
		return attrs.Resource == WorkloadsGVR().Resource, nil
	})

	var localQueueGets atomic.Int32
	client := newFakeClient(t, &interceptor.Funcs{
		Get: func(ctx context.Context, c ctrlclient.WithWatch, key ctrlclient.ObjectKey, obj ctrlclient.Object, opts ...ctrlclient.GetOption) error {
			if _, ok := obj.(*kueueapi.LocalQueue); ok {
				localQueueGets.Add(1)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, makeWorkloadObj("ns-1", "wl-1", "lq-1"), makeLocalQueue("ns-1", "lq-1", "cq-hidden"))
	h := New(client, nil, authorizer)
	h.heartbeatInterval = 0

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyIdentity, identity)
		c.Next()
	})
	h.InitializeWebSocketRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	dialer := websocket.Dialer{Subprotocols: []string{middleware.WebSocketBaseProtocol}}
	conn, _, err := dialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/ws/workload/ns-1/wl-1",
		nil,
	)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var body struct {
		Metadata         metav1.ObjectMeta `json:"metadata"`
		ClusterQueueName string            `json:"clusterQueueName"`
	}
	if err := conn.ReadJSON(&body); err != nil {
		t.Fatalf("read workload details: %v", err)
	}
	if body.Metadata.Namespace != "ns-1" || body.Metadata.Name != "wl-1" {
		t.Errorf("workload = %s/%s, want ns-1/wl-1", body.Metadata.Namespace, body.Metadata.Name)
	}
	if body.ClusterQueueName != "Unknown" {
		t.Errorf("clusterQueueName = %q, want Unknown", body.ClusterQueueName)
	}
	if got := localQueueGets.Load(); got != 0 {
		t.Errorf("LocalQueue reads = %d, want 0", got)
	}

	wantCalls := []authorizationv1.ResourceAttributes{
		middleware.ResourceAccess("get", WorkloadsGVR(), "ns-1", "wl-1"),
		middleware.ResourceAccess("watch", WorkloadsGVR(), "ns-1", "wl-1"),
		middleware.ResourceAccess("get", LocalQueuesGVR(), "ns-1", "lq-1"),
	}
	authMu.Lock()
	defer authMu.Unlock()
	if diff := cmp.Diff(wantCalls, authCalls); diff != "" {
		t.Errorf("unexpected authorization requests (-want,+got):\n%s", diff)
	}
}

func TestFetchWorkloadEvents(t *testing.T) {
	event := corev1.Event{
		Namespace: "ns-1", Name: "wl-1.123",
		InvolvedObject: corev1.ObjectReference{Name: "wl-1"},
		Reason:         "QuotaReserved",
	}

	tests := map[string]struct {
		listErr error
		want    []corev1.Event
		wantErr bool
	}{
		"returns events for the workload": {
			want: []corev1.Event{event},
		},
		"list error is returned": {
			listErr: errors.New("boom"),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gotOpts ctrlclient.ListOptions
			funcs := &interceptor.Funcs{
				List: func(_ context.Context, _ ctrlclient.WithWatch, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
					gotOpts.ApplyOptions(opts)
					if tc.listErr != nil {
						return tc.listErr
					}
					// The fake client needs an index for field selectors,
					// so return the event directly and check the options.
					list.(*corev1.EventList).Items = []corev1.Event{event}
					return nil
				},
			}
			h := New(newFakeClient(t, funcs), nil, nil)

			got, err := h.fetchWorkloadEvents(t.Context(), "ns-1", "wl-1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchWorkloadEvents() error = %v, wantErr %v", err, tc.wantErr)
			}

			if gotOpts.Namespace != "ns-1" {
				t.Errorf("namespace = %q, want %q", gotOpts.Namespace, "ns-1")
			}
			if gotOpts.FieldSelector == nil || gotOpts.FieldSelector.String() != "involvedObject.name=wl-1" {
				t.Errorf("field selector = %v, want %q", gotOpts.FieldSelector, "involvedObject.name=wl-1")
			}
			if tc.wantErr {
				return
			}
			if diff := cmp.Diff(tc.want, got.([]corev1.Event)); diff != "" {
				t.Errorf("unexpected events (-want,+got):\n%s", diff)
			}
		})
	}
}
