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
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"kueueviz/middleware"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type mockResourceEventHandlerRegistration struct{}

func (m *mockResourceEventHandlerRegistration) HasSynced() bool { return true }

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

				conn, closeServer := newTestWebSocketConnection(t, &Handlers{client: client}, dataFetcher, gvkA, gvkB)
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

				conn, closeServer := newTestWebSocketConnection(t, &Handlers{client: client}, dataFetcher, gvkA, gvkB)
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

				conn, closeServer := newTestWebSocketConnection(t, &Handlers{client: client}, dataFetcher, gvk)
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

				time.Sleep(700 * time.Millisecond)

				calls := fetchCalls.Load()
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
				case <-time.After(2 * time.Second):
					t.Fatalf("reader goroutine did not exit after connection close")
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

func newTestWebSocketConnection(
	t *testing.T,
	handlers *Handlers,
	dataFetcher func(ctx context.Context) (any, error),
	gvks ...schema.GroupVersionKind,
) (*websocket.Conn, func()) {
	t.Helper()

	router := gin.New()
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

func readMessage(t *testing.T, conn *websocket.Conn) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
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
