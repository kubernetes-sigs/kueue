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

package middleware

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"golang.org/x/time/rate"
)

type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time          { return f.now }
func (f *fakeClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

func newRouterWithReactor(t *testing.T, reactor k8stesting.ReactionFunc) (*gin.Engine, *fakeClock, *int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	callCount := 0
	cs := k8sfake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		callCount++
		return reactor(action)
	})

	auth := NewAuthenticator(cs, AuthConfig{CacheTTL: time.Minute, NegativeCacheTTL: 5 * time.Second})
	clock := &fakeClock{now: time.Now()}
	auth.clock = clock

	r := gin.New()
	r.Use(auth.Middleware())
	r.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"user": c.GetString("username")}) })
	return r, clock, &callCount
}

func makeRequest(router *gin.Engine, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func wsTokenProtocol(token string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(token))
	return fmt.Sprintf("%s, %s%s", WebSocketBaseProtocol, webSocketTokenProtocolPrefix, encoded)
}

func tokenReviewResponse(authenticated bool, username string) *authenticationv1.TokenReview {
	return &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
		Authenticated: authenticated,
		User:          authenticationv1.UserInfo{Username: username},
	}}
}

func TestExtractToken(t *testing.T) {
	tests := map[string]struct {
		authHeader string
		wsProtocol string
		wantToken  string
	}{
		"authorization header": {
			authHeader: "Bearer header-token",
			wantToken:  "header-token",
		},
		"websocket protocol": {
			wsProtocol: wsTokenProtocol("ws-token"),
			wantToken:  "ws-token",
		},
		"authorization wins": {
			authHeader: "Bearer header-token",
			wsProtocol: wsTokenProtocol("ws-token"),
			wantToken:  "header-token",
		},
		"invalid websocket protocol": {
			wsProtocol: WebSocketBaseProtocol + ", " + webSocketTokenProtocolPrefix + "!!!",
			wantToken:  "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			if tc.wsProtocol != "" {
				req.Header.Set("Sec-WebSocket-Protocol", tc.wsProtocol)
			}
			if got := extractToken(req); got != tc.wantToken {
				t.Fatalf("extractToken() = %q, want %q", got, tc.wantToken)
			}
		})
	}
}

func TestAuthMiddlewareFlow(t *testing.T) {
	tests := map[string]struct {
		headers     map[string]string
		authed      bool
		username    string
		wantStatus  int
		wantCalls   int
		wantWWWAuth bool
	}{
		"valid authorization header": {
			headers:    map[string]string{"Authorization": "Bearer good-token"},
			authed:     true,
			username:   "admin",
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		"valid websocket protocol token": {
			headers:    map[string]string{"Sec-WebSocket-Protocol": wsTokenProtocol("ws-good-token")},
			authed:     true,
			username:   "admin",
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		"invalid token": {
			headers:     map[string]string{"Authorization": "Bearer bad-token"},
			authed:      false,
			wantStatus:  http.StatusUnauthorized,
			wantCalls:   1,
			wantWWWAuth: true,
		},
		"missing token": {
			wantStatus:  http.StatusUnauthorized,
			wantCalls:   0,
			wantWWWAuth: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			router, _, callCount := newRouterWithReactor(t, func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, tokenReviewResponse(tc.authed, tc.username), nil
			})

			w := makeRequest(router, "/test", tc.headers)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if *callCount != tc.wantCalls {
				t.Fatalf("token review calls = %d, want %d", *callCount, tc.wantCalls)
			}
			if tc.wantWWWAuth && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want %q", w.Header().Get("WWW-Authenticate"), "Bearer")
			}
		})
	}
}

func TestAuthMiddlewareCacheExpiry(t *testing.T) {
	tests := map[string]struct {
		authed     bool
		token      string
		advance1   time.Duration
		advance2   time.Duration
		wantStatus int
	}{
		"positive cache expires": {
			authed: true, token: "expiry-token", advance1: 30 * time.Second, advance2: 31 * time.Second,
			wantStatus: http.StatusOK,
		},
		"negative cache expires": {
			authed: false, token: "bad-expiry-token", advance1: 3 * time.Second, advance2: 3 * time.Second,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			router, clock, callCount := newRouterWithReactor(t, func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, tokenReviewResponse(tc.authed, "user"), nil
			})
			headers := map[string]string{"Authorization": "Bearer " + tc.token}

			if w := makeRequest(router, "/test", headers); w.Code != tc.wantStatus {
				t.Fatalf("request 1: status = %d, want %d", w.Code, tc.wantStatus)
			}
			if *callCount != 1 {
				t.Fatalf("after request 1: calls = %d, want 1", *callCount)
			}

			clock.Advance(tc.advance1)
			if w := makeRequest(router, "/test", headers); w.Code != tc.wantStatus {
				t.Fatalf("request 2: status = %d, want %d", w.Code, tc.wantStatus)
			}
			if *callCount != 1 {
				t.Fatalf("after request 2: calls = %d, want 1", *callCount)
			}

			clock.Advance(tc.advance2)
			if w := makeRequest(router, "/test", headers); w.Code != tc.wantStatus {
				t.Fatalf("request 3: status = %d, want %d", w.Code, tc.wantStatus)
			}
			if *callCount != 2 {
				t.Fatalf("after request 3: calls = %d, want 2", *callCount)
			}
		})
	}
}

// TestTokenReviewCacheLRUEviction verifies that when the cache reaches its
// capacity the least-recently-used entry is evicted, keeping memory bounded.
func TestTokenReviewCacheLRUEviction(t *testing.T) {
	const capacity = 3
	c := newTokenReviewCache(capacity)
	now := time.Now()
	expires := now.Add(time.Hour)

	// Fill cache to capacity with tokens t1, t2, t3.
	for _, key := range []string{"t1", "t2", "t3"} {
		c.Set(key, cacheEntry{authenticated: true, expiresAt: expires})
	}

	// Access t1 to make it most-recently-used (t2 becomes LRU).
	if _, ok := c.Get("t1", now); !ok {
		t.Fatal("expected t1 in cache")
	}

	// Insert t4 — should evict the LRU entry (t2).
	c.Set("t4", cacheEntry{authenticated: true, expiresAt: expires})

	if c.order.Len() != capacity {
		t.Fatalf("cache length = %d, want %d", c.order.Len(), capacity)
	}
	if _, ok := c.Get("t2", now); ok {
		t.Fatal("expected t2 to be evicted (LRU)")
	}
	for _, key := range []string{"t1", "t3", "t4"} {
		if _, ok := c.Get(key, now); !ok {
			t.Fatalf("expected %s in cache after LRU eviction", key)
		}
	}
}

// TestTokenReviewCacheCapacityBounded verifies that inserting more tokens than
// the cache capacity never grows the internal map beyond the limit.
func TestTokenReviewCacheCapacityBounded(t *testing.T) {
	const capacity = 5
	c := newTokenReviewCache(capacity)
	expires := time.Now().Add(time.Hour)

	for i := range 100 {
		c.Set(fmt.Sprintf("token-%d", i), cacheEntry{authenticated: true, expiresAt: expires})
	}

	c.mu.Lock()
	size := len(c.items)
	c.mu.Unlock()

	if size > capacity {
		t.Fatalf("cache size = %d, want ≤ %d", size, capacity)
	}
}

// TestRateLimiterMiddleware verifies that requests exceeding the burst limit
// receive 429 Too Many Requests.
func TestRateLimiterMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Allow only 1 request per second, burst of 1.
	r.Use(RateLimiter(rate.Limit(1), 1))
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	// First request should succeed (consumes the burst token).
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", w.Code)
	}

	// Immediate second request should be rate-limited.
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: status = %d, want 429", w2.Code)
	}
}
