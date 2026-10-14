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
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"kueueviz/middleware"
)

func TestSecondaryWebSocketInformersRespectWatchAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	endpoints := map[string]secondaryWatchEndpoint{
		"workloads dashboard": {
			path:    "/ws/workloads/dashboard",
			primary: WorkloadsGVK(),
			secondary: []secondaryWatch{
				{gvk: PodsGVK(), gvr: PodsGVR()},
				{gvk: ClusterQueuesGVK(), gvr: ClusterQueuesGVR()},
				{gvk: LocalQueuesGVK(), gvr: LocalQueuesGVR()},
				{gvk: ResourceFlavorsGVK(), gvr: ResourceFlavorsGVR()},
			},
			wantCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("list", WorkloadsGVR(), "", ""),
				middleware.ResourceAccess("watch", WorkloadsGVR(), "", ""),
				middleware.ResourceAccess("watch", PodsGVR(), "", ""),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("watch", LocalQueuesGVR(), "", ""),
				middleware.ResourceAccess("watch", ResourceFlavorsGVR(), "", ""),
				middleware.ResourceAccess("list", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("list", ResourceFlavorsGVR(), "", ""),
				middleware.ResourceAccess("list", LocalQueuesGVR(), "", ""),
			},
			wantErrorCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("list", WorkloadsGVR(), "", ""),
				middleware.ResourceAccess("watch", WorkloadsGVR(), "", ""),
				middleware.ResourceAccess("watch", PodsGVR(), "", ""),
			},
		},
		"cohorts": {
			path:    "/ws/cohorts",
			primary: CohortsGVK(),
			secondary: []secondaryWatch{
				{gvk: ClusterQueuesGVK(), gvr: ClusterQueuesGVR()},
			},
			wantCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("list", CohortsGVR(), "", ""),
				middleware.ResourceAccess("watch", CohortsGVR(), "", ""),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
			},
			wantErrorCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("list", CohortsGVR(), "", ""),
				middleware.ResourceAccess("watch", CohortsGVR(), "", ""),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
			},
		},
		"cohort details": {
			path:    "/ws/cohort/cohort-a",
			primary: CohortsGVK(),
			secondary: []secondaryWatch{
				{gvk: ClusterQueuesGVK(), gvr: ClusterQueuesGVR()},
			},
			wantCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("get", CohortsGVR(), "", "cohort-a"),
				middleware.ResourceAccess("watch", CohortsGVR(), "", "cohort-a"),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("list", ClusterQueuesGVR(), "", ""),
			},
			wantErrorCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("get", CohortsGVR(), "", "cohort-a"),
				middleware.ResourceAccess("watch", CohortsGVR(), "", "cohort-a"),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
			},
		},
		"cluster queue details": {
			path:    "/ws/cluster-queue/cq-a",
			primary: ClusterQueuesGVK(),
			secondary: []secondaryWatch{
				{gvk: LocalQueuesGVK(), gvr: LocalQueuesGVR()},
			},
			wantCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("get", ClusterQueuesGVR(), "", "cq-a"),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", "cq-a"),
				middleware.ResourceAccess("watch", LocalQueuesGVR(), "", ""),
			},
			wantErrorCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("get", ClusterQueuesGVR(), "", "cq-a"),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", "cq-a"),
				middleware.ResourceAccess("watch", LocalQueuesGVR(), "", ""),
			},
		},
		"resource flavor details": {
			path:    "/ws/resource-flavor/flavor-a",
			primary: ResourceFlavorsGVK(),
			secondary: []secondaryWatch{
				{gvk: ClusterQueuesGVK(), gvr: ClusterQueuesGVR()},
				{gvk: NodesGVK(), gvr: NodesGVR()},
			},
			wantCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("get", ResourceFlavorsGVR(), "", "flavor-a"),
				middleware.ResourceAccess("watch", ResourceFlavorsGVR(), "", "flavor-a"),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("watch", NodesGVR(), "", ""),
				middleware.ResourceAccess("list", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("list", NodesGVR(), "", ""),
			},
			wantErrorCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("get", ResourceFlavorsGVR(), "", "flavor-a"),
				middleware.ResourceAccess("watch", ResourceFlavorsGVR(), "", "flavor-a"),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
			},
		},
	}

	scenarios := map[string]secondaryWatchScenario{
		"without authorizer registers every informer": {},
		"allowed secondary watches keep live updates": {
			useAuthorizer: true,
		},
		"denied secondary watches register no handler": {
			useAuthorizer: true,
			denySecondary: true,
		},
		"secondary watch authorization error fails closed": {
			useAuthorizer: true,
			authErr:       true,
		},
	}

	for endpointName, endpoint := range endpoints {
		for scenarioName, scenario := range scenarios {
			t.Run(endpointName+"/"+scenarioName, func(t *testing.T) {
				denied := map[string]struct{}{}
				errored := map[string]struct{}{}
				if scenario.denySecondary {
					for _, watch := range endpoint.secondary {
						denied[watch.gvr.Resource] = struct{}{}
					}
				}
				if scenario.authErr {
					errored[endpoint.secondary[0].gvr.Resource] = struct{}{}
				}

				wantRegistered := []schema.GroupVersionKind{endpoint.primary}
				for _, watch := range endpoint.secondary {
					wantRegistered = append(wantRegistered, watch.gvk)
				}
				wantCalls := endpoint.wantCalls
				if !scenario.useAuthorizer {
					wantCalls = nil
				}
				if scenario.denySecondary {
					wantRegistered = []schema.GroupVersionKind{endpoint.primary}
				}
				if scenario.authErr {
					wantRegistered = nil
					wantCalls = endpoint.wantErrorCalls
				}

				runSecondaryWatchCase(t, secondaryWatchCase{
					path:           endpoint.path,
					informers:      endpoint.informerGVKs(),
					useAuthorizer:  scenario.useAuthorizer,
					denied:         denied,
					errored:        errored,
					wantCalls:      wantCalls,
					wantRegistered: wantRegistered,
					wantClose:      scenario.authErr,
					wantLiveUpdate: !scenario.denySecondary && !scenario.authErr,
				})
			})
		}
	}
}

func TestSecondaryWebSocketInformersRegisterOnlyAllowedWatches(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := map[string]secondaryWatchCase{
		"dashboard keeps allowed secondary informers and drops denied ones": {
			path: "/ws/workloads/dashboard",
			informers: []schema.GroupVersionKind{
				WorkloadsGVK(),
				PodsGVK(),
				ClusterQueuesGVK(),
				LocalQueuesGVK(),
				ResourceFlavorsGVK(),
			},
			useAuthorizer: true,
			denied: map[string]struct{}{
				ClusterQueuesGVR().Resource:   {},
				ResourceFlavorsGVR().Resource: {},
			},
			wantCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("list", WorkloadsGVR(), "", ""),
				middleware.ResourceAccess("watch", WorkloadsGVR(), "", ""),
				middleware.ResourceAccess("watch", PodsGVR(), "", ""),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("watch", LocalQueuesGVR(), "", ""),
				middleware.ResourceAccess("watch", ResourceFlavorsGVR(), "", ""),
				middleware.ResourceAccess("list", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("list", ResourceFlavorsGVR(), "", ""),
				middleware.ResourceAccess("list", LocalQueuesGVR(), "", ""),
			},
			wantRegistered: []schema.GroupVersionKind{
				WorkloadsGVK(),
				PodsGVK(),
				LocalQueuesGVK(),
			},
			wantLiveUpdate: true,
		},
		"resource flavor keeps allowed secondary informers and drops denied ones": {
			path: "/ws/resource-flavor/flavor-a",
			informers: []schema.GroupVersionKind{
				ResourceFlavorsGVK(),
				ClusterQueuesGVK(),
				NodesGVK(),
			},
			useAuthorizer: true,
			denied: map[string]struct{}{
				NodesGVR().Resource: {},
			},
			wantCalls: []authorizationv1.ResourceAttributes{
				middleware.ResourceAccess("get", ResourceFlavorsGVR(), "", "flavor-a"),
				middleware.ResourceAccess("watch", ResourceFlavorsGVR(), "", "flavor-a"),
				middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("watch", NodesGVR(), "", ""),
				middleware.ResourceAccess("list", ClusterQueuesGVR(), "", ""),
				middleware.ResourceAccess("list", NodesGVR(), "", ""),
			},
			wantRegistered: []schema.GroupVersionKind{
				ResourceFlavorsGVK(),
				ClusterQueuesGVK(),
			},
			wantLiveUpdate: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			runSecondaryWatchCase(t, tc)
		})
	}
}

type secondaryWatchEndpoint struct {
	path           string
	primary        schema.GroupVersionKind
	secondary      []secondaryWatch
	wantCalls      []authorizationv1.ResourceAttributes
	wantErrorCalls []authorizationv1.ResourceAttributes
}

func (e secondaryWatchEndpoint) informerGVKs() []schema.GroupVersionKind {
	gvks := []schema.GroupVersionKind{e.primary}
	for _, watch := range e.secondary {
		gvks = append(gvks, watch.gvk)
	}
	return gvks
}

type secondaryWatchScenario struct {
	useAuthorizer bool
	denySecondary bool
	authErr       bool
}

type secondaryWatchCase struct {
	path           string
	informers      []schema.GroupVersionKind
	useAuthorizer  bool
	denied         map[string]struct{}
	errored        map[string]struct{}
	wantCalls      []authorizationv1.ResourceAttributes
	wantRegistered []schema.GroupVersionKind
	wantClose      bool
	wantLiveUpdate bool
}

func runSecondaryWatchCase(t *testing.T, tc secondaryWatchCase) {
	t.Helper()

	informers := make(map[schema.GroupVersionKind]*mockInformer, len(tc.informers))
	for _, gvk := range tc.informers {
		informers[gvk] = newMockInformer()
	}

	var got []authorizationv1.ResourceAttributes
	var authorizer middleware.Authorizer
	if tc.useAuthorizer {
		authorizer = authorizerFunc(func(attrs authorizationv1.ResourceAttributes) (bool, error) {
			got = append(got, attrs)
			if attrs.Verb != "watch" || attrs.Namespace != "" || attrs.Name != "" {
				return true, nil
			}
			if _, ok := tc.errored[attrs.Resource]; ok {
				return false, errors.New("sar failed")
			}
			if _, ok := tc.denied[attrs.Resource]; ok {
				return false, nil
			}
			return true, nil
		})
	}

	handlers := New(newMockClient(informers), nil, authorizer)
	handlers.heartbeatInterval = 0
	conn := dialWebSocketRoute(t, handlers, tc.path)

	if tc.wantClose {
		assertWebSocketClose(t, conn, middleware.WSCloseServiceUnavailable, "authorization service unavailable")
		assertInformerHandlerCounts(t, informers, nil)
		if diff := cmp.Diff(tc.wantCalls, got); diff != "" {
			t.Fatalf("authorization checks mismatch (-want +got):\n%s", diff)
		}
		return
	}

	readTextMessage(t, conn)
	// Later informer events fetch the snapshot again. Compare the checks from
	// the initial response, which is where watch gating and list filtering run.
	if diff := cmp.Diff(tc.wantCalls, got); diff != "" {
		t.Fatalf("authorization checks mismatch (-want +got):\n%s", diff)
	}

	if tc.wantLiveUpdate {
		informer, ok := liveSecondaryInformer(informers, tc.wantRegistered)
		if !ok {
			t.Fatal("wantLiveUpdate requires a registered secondary informer")
		}
		waitUntil(t, testTimeout, testPollInterval, func() bool {
			return informer.AddEventHandlerCallCount() == 1
		}, "secondary informer handler was not registered")
		informer.triggerAdd("changed")
		readTextMessage(t, conn)
	} else if len(tc.wantRegistered) > 0 {
		primary := informers[tc.wantRegistered[0]]
		waitUntil(t, testTimeout, testPollInterval, func() bool {
			return primary.AddEventHandlerCallCount() == 1
		}, "primary informer handler was not registered")
		if err := conn.Close(); err != nil {
			t.Fatalf("close websocket: %v", err)
		}
		waitUntil(t, testTimeout, testPollInterval, func() bool {
			return primary.RemoveEventHandlerCallCount() == 1
		}, "primary informer handler was not removed")
	}

	assertInformerHandlerCounts(t, informers, tc.wantRegistered)
}

func liveSecondaryInformer(informers map[schema.GroupVersionKind]*mockInformer, registered []schema.GroupVersionKind) (*mockInformer, bool) {
	if len(registered) < 2 {
		return nil, false
	}
	informer, ok := informers[registered[1]]
	return informer, ok
}

func assertInformerHandlerCounts(t *testing.T, informers map[schema.GroupVersionKind]*mockInformer, wantRegistered []schema.GroupVersionKind) {
	t.Helper()

	want := make(map[schema.GroupVersionKind]int, len(wantRegistered))
	for _, gvk := range wantRegistered {
		want[gvk] = 1
	}
	for gvk, informer := range informers {
		if got := informer.AddEventHandlerCallCount(); got != want[gvk] {
			t.Errorf("%s informer handlers registered = %d, want %d", gvk.Kind, got, want[gvk])
		}
	}
}

func assertWebSocketClose(t *testing.T, conn *websocket.Conn, code int, reason string) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, _, err := conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("ReadMessage error = %v, want close %d", err, code)
	}
	if closeErr.Code != code || closeErr.Text != reason {
		t.Fatalf("close = (%d, %q), want (%d, %q)", closeErr.Code, closeErr.Text, code, reason)
	}
}

func readTextMessage(t *testing.T, conn *websocket.Conn) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket message: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("message type = %d, want %d", messageType, websocket.TextMessage)
	}
	if len(payload) == 0 {
		t.Fatal("websocket payload is empty")
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("reset read deadline: %v", err)
	}
}

func dialWebSocketRoute(t *testing.T, handlers *Handlers, path string) *websocket.Conn {
	t.Helper()

	router := gin.New()
	handlers.InitializeWebSocketRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + path
	dialer := websocket.Dialer{Subprotocols: []string{middleware.WebSocketBaseProtocol}}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		var body []byte
		if resp != nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		t.Fatalf("dial websocket %s: %v body=%s", path, err, body)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
