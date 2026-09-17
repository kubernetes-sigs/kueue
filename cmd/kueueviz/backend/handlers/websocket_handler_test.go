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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"kueueviz/middleware"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var closedChan = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

const (
	// testTokenRevalidationInterval is a short interval used in tests to quickly
	// trigger the token re-validation ticker without waiting 30 seconds.
	testTokenRevalidationInterval = 100 * time.Millisecond

	// testHeartbeatInterval is a short interval used in tests to quickly
	// trigger WebSocket heartbeat pings without waiting 30 seconds.
	testHeartbeatInterval = 100 * time.Millisecond
	// testPollInterval is the polling frequency used in waitUntil calls.
	testPollInterval = 10 * time.Millisecond

	// testTimeout is the maximum duration tests will wait for an async condition.
	testTimeout = 2 * time.Second
)

type mockTokenValidator struct {
	mu    sync.Mutex
	valid bool
	calls int
}

func (m *mockTokenValidator) ValidateToken(ctx context.Context, token string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.valid, nil
}

func (m *mockTokenValidator) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *mockTokenValidator) setValid(valid bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.valid = valid
}

type alwaysDone struct{}

func (alwaysDone) Name() string {
	return "mock"
}

func (alwaysDone) Done() <-chan struct{} {
	return closedChan
}

type mockResourceEventHandlerRegistration struct{}

func (m *mockResourceEventHandlerRegistration) HasSynced() bool { return true }
func (m *mockResourceEventHandlerRegistration) HasSyncedChecker() toolscache.DoneChecker {
	return alwaysDone{}
}

type mockInformer struct {
	mu sync.Mutex

	handlers       []toolscache.ResourceEventHandler
	registrations  map[toolscache.ResourceEventHandlerRegistration]struct{}
	addHandlerCall int
	removeCall     int
}

func newMockInformer() *mockInformer {
	return &mockInformer{
		registrations: make(map[toolscache.ResourceEventHandlerRegistration]struct{}),
	}
}

func (m *mockInformer) AddEventHandler(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.addHandlerCall++
	m.handlers = append(m.handlers, handler)
	registration := &mockResourceEventHandlerRegistration{}
	m.registrations[registration] = struct{}{}

	return registration, nil
}

func (m *mockInformer) AddEventHandlerWithResyncPeriod(handler toolscache.ResourceEventHandler, _ time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return m.AddEventHandler(handler)
}

func (m *mockInformer) AddEventHandlerWithOptions(handler toolscache.ResourceEventHandler, _ toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	return m.AddEventHandler(handler)
}

func (m *mockInformer) RemoveEventHandler(handle toolscache.ResourceEventHandlerRegistration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.registrations[handle]; !ok {
		return errors.New("unknown registration")
	}

	delete(m.registrations, handle)
	m.removeCall++

	return nil
}

func (m *mockInformer) AddIndexers(_ toolscache.Indexers) error { return nil }

func (m *mockInformer) HasSynced() bool { return true }

func (m *mockInformer) HasSyncedChecker() toolscache.DoneChecker { return alwaysDone{} }

func (m *mockInformer) IsStopped() bool { return false }

func (m *mockInformer) AddEventHandlerCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.addHandlerCall
}

func (m *mockInformer) RemoveEventHandlerCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.removeCall
}

func (m *mockInformer) triggerAdd(obj any) {
	m.mu.Lock()
	handlers := append([]toolscache.ResourceEventHandler(nil), m.handlers...)
	m.mu.Unlock()

	for _, handler := range handlers {
		handler.OnAdd(obj, false)
	}
}

type mockClient struct {
	mu sync.Mutex

	informersByGVK map[schema.GroupVersionKind]*mockInformer
	getCalls       []schema.GroupVersionKind
}

func newMockClient(informersByGVK map[schema.GroupVersionKind]*mockInformer) *mockClient {
	return &mockClient{informersByGVK: informersByGVK}
}

func (m *mockClient) Get(_ context.Context, _ ctrlclient.ObjectKey, _ ctrlclient.Object, _ ...ctrlclient.GetOption) error {
	return nil
}

func (m *mockClient) List(_ context.Context, _ ctrlclient.ObjectList, _ ...ctrlclient.ListOption) error {
	return nil
}

func (m *mockClient) GetInformerForKind(_ context.Context, gvk schema.GroupVersionKind, _ ...ctrlcache.InformerGetOption) (ctrlcache.Informer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.getCalls = append(m.getCalls, gvk)

	informer, ok := m.informersByGVK[gvk]
	if !ok {
		return nil, fmt.Errorf("informer not found for GVK %v", gvk)
	}

	return informer, nil
}

func (m *mockClient) GetInformerCallCount(gvk schema.GroupVersionKind) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	for _, calledGVK := range m.getCalls {
		if calledGVK == gvk {
			count++
		}
	}

	return count
}

func TestWebSocketHandleInformerUpdates(t *testing.T) {
	tests := map[string]struct {
		run func(t *testing.T)
	}{
		"registers event handlers for all provided GVKs": {
			run: func(t *testing.T) {
				gvkA := schema.GroupVersionKind{Group: "group-a", Version: "v1", Kind: "KindA"}
				gvkB := schema.GroupVersionKind{Group: "group-b", Version: "v1", Kind: "KindB"}

				informerA := newMockInformer()
				informerB := newMockInformer()
				client := newMockClient(map[schema.GroupVersionKind]*mockInformer{
					gvkA: informerA,
					gvkB: informerB,
				})

				var fetchCalls atomic.Int64
				dataFetcher := func(_ context.Context) (any, error) {
					return map[string]int64{"call": fetchCalls.Add(1)}, nil
				}

				h := New(client, nil, nil)
				conn, closeServer := newTestWebSocketConnection(t, h, dataFetcher, gvkA, gvkB)
				defer closeServer()
				defer conn.Close()

				readMessage(t, conn)

				waitUntil(t, 2*time.Second, 10*time.Millisecond, func() bool {
					return informerA.AddEventHandlerCallCount() == 1 && informerB.AddEventHandlerCallCount() == 1
				}, "expected AddEventHandler to be called exactly once for each informer")

				if got := client.GetInformerCallCount(gvkA); got < 1 {
					t.Fatalf("GetInformerForKind calls for gvkA = %d, want >= 1", got)
				}
				if got := client.GetInformerCallCount(gvkB); got < 1 {
					t.Fatalf("GetInformerForKind calls for gvkB = %d, want >= 1", got)
				}
			},
		},
		"closes connection when token becomes invalid": {
			run: func(t *testing.T) {
				gvk := schema.GroupVersionKind{Group: "group", Version: "v1", Kind: "Kind"}
				informer := newMockInformer()
				client := newMockClient(map[schema.GroupVersionKind]*mockInformer{gvk: informer})

				validator := &mockTokenValidator{valid: true}

				var fetchCalls atomic.Int64
				dataFetcher := func(_ context.Context) (any, error) {
					return map[string]int64{"call": fetchCalls.Add(1)}, nil
				}

				h := New(client, validator, nil)
				h.tokenRevalidationInterval = testTokenRevalidationInterval
				conn, closeServer := newTestWebSocketConnectionWithToken(t, h, "test-token", dataFetcher, gvk)
				defer closeServer()

				// Read initial message
				readMessage(t, conn)

				// Token should be validated successfully initially (by the ticker)
				waitUntil(t, testTimeout, testPollInterval, func() bool {
					return validator.getCalls() >= 1
				}, "expected token validator to be called")

				// Now simulate token expiration/revocation
				validator.setValid(false)

				// The connection should be closed by the server
				errChan := make(chan error, 1)
				go func() {
					_, _, err := conn.ReadMessage()
					errChan <- err
				}()

				select {
				case err := <-errChan:
					if err == nil {
						t.Fatalf("expected read error due to connection closure, but got none")
					}
					if closeErr, ok := errors.AsType[*websocket.CloseError](err); ok {
						if closeErr.Code != websocket.ClosePolicyViolation {
							t.Fatalf("expected close code %d, got %d", websocket.ClosePolicyViolation, closeErr.Code)
						}
					} else {
						t.Fatalf("expected CloseError, got %v", err)
					}
				case <-time.After(testTimeout):
					t.Fatalf("timeout waiting for connection to close after token expiration")
				}
			},
		},
		"removes event handlers on context cancellation": {
			run: func(t *testing.T) {
				gvkA := schema.GroupVersionKind{Group: "group-a", Version: "v1", Kind: "KindA"}
				gvkB := schema.GroupVersionKind{Group: "group-b", Version: "v1", Kind: "KindB"}

				informerA := newMockInformer()
				informerB := newMockInformer()
				client := newMockClient(map[schema.GroupVersionKind]*mockInformer{
					gvkA: informerA,
					gvkB: informerB,
				})

				dataFetcher := func(_ context.Context) (any, error) {
					return map[string]string{"status": "ok"}, nil
				}

				h := New(client, nil, nil)
				conn, closeServer := newTestWebSocketConnection(t, h, dataFetcher, gvkA, gvkB)
				defer closeServer()

				readMessage(t, conn)

				waitUntil(t, 2*time.Second, 10*time.Millisecond, func() bool {
					return informerA.AddEventHandlerCallCount() == 1 && informerB.AddEventHandlerCallCount() == 1
				}, "expected handlers to be registered before cancellation")

				if err := conn.Close(); err != nil {
					t.Fatalf("close websocket connection: %v", err)
				}

				waitUntil(t, 2*time.Second, 10*time.Millisecond, func() bool {
					return informerA.RemoveEventHandlerCallCount() == 1 && informerB.RemoveEventHandlerCallCount() == 1
				}, "expected RemoveEventHandler to be called once per informer after context cancellation")
			},
		},
		"debounces rapid informer events into fewer sendData calls": {
			run: func(t *testing.T) {
				gvk := schema.GroupVersionKind{Group: "group", Version: "v1", Kind: "Kind"}

				informer := newMockInformer()
				client := newMockClient(map[schema.GroupVersionKind]*mockInformer{gvk: informer})

				var fetchCalls atomic.Int64
				dataFetcher := func(_ context.Context) (any, error) {
					return map[string]int64{"call": fetchCalls.Add(1)}, nil
				}

				h := New(client, nil, nil)
				conn, closeServer := newTestWebSocketConnection(t, h, dataFetcher, gvk)
				defer closeServer()
				defer conn.Close()

				readMessage(t, conn)

				drainDone := make(chan struct{})
				go func() {
					defer close(drainDone)
					for {
						if _, _, err := conn.ReadMessage(); err != nil {
							return
						}
					}
				}()

				waitUntil(t, 2*time.Second, 10*time.Millisecond, func() bool {
					return informer.AddEventHandlerCallCount() == 1
				}, "expected handler registration before firing informer events")

				const events = 20
				for range events {
					informer.triggerAdd(struct{}{})
				}

				waitUntil(t, 2*time.Second, 10*time.Millisecond, func() bool {
					return fetchCalls.Load() > 1
				}, "expected at least one update after burst of informer events")

				calls := waitForStableCount(t, testTimeout, 10*time.Millisecond, 3*debounceDelay, fetchCalls.Load)
				if calls >= events+1 {
					t.Fatalf("data fetch calls = %d, want less than %d due to debounce", calls, events+1)
				}
				if calls > 4 {
					t.Fatalf("data fetch calls = %d, want <= 4 for debounced burst", calls)
				}

				if err := conn.Close(); err != nil {
					t.Fatalf("close websocket connection: %v", err)
				}
				select {
				case <-drainDone:
				case <-time.After(testTimeout):
					t.Fatalf("reader goroutine did not exit after connection close")
				}
			},
		},
		"sends websocket heartbeat ping": {
			run: func(t *testing.T) {
				gvk := schema.GroupVersionKind{
					Group:   "group",
					Version: "v1",
					Kind:    "Kind",
				}

				informer := newMockInformer()
				client := newMockClient(
					map[schema.GroupVersionKind]*mockInformer{
						gvk: informer,
					},
				)

				dataFetcher := func(_ context.Context) (any, error) {
					return map[string]string{"status": "ok"}, nil
				}

				h := New(client, nil, nil)
				h.heartbeatInterval = testHeartbeatInterval
				conn, closeServer := newTestWebSocketConnection(t, h, dataFetcher, gvk)
				defer closeServer()
				defer conn.Close()

				// Consume the initial data snapshot.
				readMessage(t, conn)

				pingReceived := make(chan struct{}, 1)
				defaultPingHandler := conn.PingHandler()

				conn.SetPingHandler(func(appData string) error {
					select {
					case pingReceived <- struct{}{}:
					default:
					}

					return defaultPingHandler(appData)
				})

				readErr := make(chan error, 1)
				go func() {
					for {
						if _, _, err := conn.ReadMessage(); err != nil {
							readErr <- err
							return
						}
					}
				}()

				select {
				case <-pingReceived:
				case err := <-readErr:
					t.Fatalf("read websocket message: %v", err)
				case <-time.After(testTimeout):
					t.Fatalf("timeout waiting for WebSocket heartbeat ping")
				}
			},
		},
		"enforces websocket read limit": {
			run: func(t *testing.T) {
				gvk := schema.GroupVersionKind{Group: "group", Version: "v1", Kind: "Kind"}

				informer := newMockInformer()
				client := newMockClient(map[schema.GroupVersionKind]*mockInformer{gvk: informer})

				dataFetcher := func(_ context.Context) (any, error) {
					return map[string]string{"status": "ok"}, nil
				}

				h := New(client, nil, nil)
				conn, closeServer := newTestWebSocketConnection(t, h, dataFetcher, gvk)
				defer closeServer()
				defer conn.Close()

				readMessage(t, conn)

				payload := make([]byte, 9000)

				err := conn.WriteMessage(websocket.BinaryMessage, payload)
				if err != nil {
					t.Fatalf("failed to write 9KB message to server: %v", err)
				}

				if err := conn.SetReadDeadline(time.Now().Add(testTimeout)); err != nil {
					t.Fatalf("set read deadline: %v", err)
				}

				_, _, err = conn.ReadMessage()

				if err == nil {
					t.Fatalf("expected read error due to limit, but got nil")
				}

				if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) && !strings.Contains(err.Error(), "close 1009") {
					t.Fatalf("expected CloseMessageTooBig (1009), got: %v", err)
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			tc.run(t)
		})
	}
}

func TestAuthMiddlewareAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := map[string]struct {
		authorizer middleware.Authorizer
		wantCode   int
	}{
		"denied caller is rejected before upgrade": {
			authorizer: stubAuthorizer{allowed: false},
			wantCode:   http.StatusForbidden,
		},
		"authorization backend error surfaces as 503": {
			authorizer: stubAuthorizer{allowed: false, err: errors.New("sar failed")},
			wantCode:   http.StatusServiceUnavailable,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			authMW := middleware.RequireAuthorization(tc.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
				return []authorizationv1.ResourceAttributes{middleware.ResourceAccess("list", WorkloadsGVR(), "", "")}
			})
			router := gin.New()
			router.GET("/ws/test", authMW, func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws/test", nil))

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

func newTestWebSocketConnectionWithToken(
	t *testing.T,
	handlers *Handlers,
	token string,
	dataFetcher func(ctx context.Context) (any, error),
	gvks ...schema.GroupVersionKind,
) (*websocket.Conn, func()) {
	t.Helper()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		if token != "" {
			c.Set("token", token)
		}
		c.Next()
	})
	router.GET("/ws/test", handlers.GenericWebSocketHandler(dataFetcher, gvks...))

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/test"
	dialer := websocket.Dialer{Subprotocols: []string{middleware.WebSocketBaseProtocol}}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	closeServer := func() {
		server.Close()
	}

	return conn, closeServer
}

func newTestWebSocketConnection(
	t *testing.T,
	handlers *Handlers,
	dataFetcher func(ctx context.Context) (any, error),
	gvks ...schema.GroupVersionKind,
) (*websocket.Conn, func()) {
	return newTestWebSocketConnectionWithToken(t, handlers, "", dataFetcher, gvks...)
}

func readMessage(t *testing.T, conn *websocket.Conn) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read websocket message: %v", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("reset read deadline: %v", err)
	}
}

func waitUntil(t *testing.T, timeout, interval time.Duration, condition func() bool, failMessage string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(interval)
	}

	t.Fatalf("timeout after %v: %s", timeout, failMessage)
}

// waitForStableCount polls count until it stops changing for stableFor, then
// returns the settled value. This is used instead of a fixed sleep to wait
// out a debounce window: it fails fast once the value has genuinely
// stabilized rather than guessing a fixed margin, and gives an informative
// failure message (last observed value) if it never stabilizes.
func waitForStableCount(t *testing.T, timeout, interval, stableFor time.Duration, count func() int64) int64 {
	t.Helper()

	deadline := time.Now().Add(timeout)
	last := count()
	lastChange := time.Now()
	for {
		current := count()
		if current != last {
			last = current
			lastChange = time.Now()
		} else if time.Since(lastChange) >= stableFor {
			return last
		}

		if time.Now().After(deadline) {
			t.Fatalf("timeout after %v waiting for count to stabilize: last value = %d", timeout, last)
		}
		time.Sleep(interval)
	}
}
