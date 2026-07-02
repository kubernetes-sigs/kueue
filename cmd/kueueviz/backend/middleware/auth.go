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
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// WebSocketBaseProtocol is the mandatory subprotocol used by frontend and backend.
	WebSocketBaseProtocol = "kueueviz.v1"

	webSocketTokenProtocolPrefix = "kueueviz.auth."

	// defaultCacheSize is the maximum number of token entries held in the LRU
	// cache. When the limit is reached the least-recently-used entry is evicted,
	// bounding memory regardless of how many unique (invalid) tokens an attacker
	// sends.
	defaultCacheSize = 1024
)

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type cacheEntry struct {
	authenticated bool
	username      string
	expiresAt     time.Time
}

type AuthConfig struct {
	Audiences        []string
	CacheTTL         time.Duration
	NegativeCacheTTL time.Duration
	// CacheSize is the maximum number of distinct tokens held in the LRU cache.
	// Zero means defaultCacheSize (1024).
	CacheSize int
}

type Authenticator struct {
	clientset kubernetes.Interface
	config    AuthConfig
	cache     *tokenReviewCache
	clock     Clock
}

func NewAuthenticator(clientset kubernetes.Interface, config AuthConfig) *Authenticator {
	size := config.CacheSize
	if size <= 0 {
		size = defaultCacheSize
	}
	clock := Clock(realClock{})
	return &Authenticator{
		clientset: clientset,
		config:    config,
		cache:     newTokenReviewCache(size, clock),
		clock:     clock,
	}
}

func (a *Authenticator) Stop() {
	a.cache.Stop()
}

// RateLimiter returns a gin middleware that enforces a per-client-IP
// token-bucket rate limit. Each source IP gets its own independent bucket so
// an attacker flooding from one IP cannot drain the budget for legitimate
// clients. Requests that exceed the per-IP limit receive 429 Too Many Requests
// before they reach the authentication logic, preventing TokenReview
// amplification.
//
//   - r: steady-state requests per second allowed per IP.
//   - burst: maximum burst size (peak requests from a single IP at once).
func RateLimiter(r rate.Limit, burst int) gin.HandlerFunc {
	var (
		mu       sync.Mutex
		limiters = make(map[string]*rate.Limiter)
	)
	getLimiter := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		if l, ok := limiters[ip]; ok {
			return l
		}
		l := rate.NewLimiter(r, burst)
		limiters[ip] = l
		return l
	}
	return func(c *gin.Context) {
		// Prefer X-Forwarded-For (set by trusted reverse proxies) over
		// RemoteAddr so that clients behind a load balancer are keyed
		// correctly rather than all sharing the proxy's IP.
		ip := c.GetHeader("X-Forwarded-For")
		if ip == "" {
			ip = c.ClientIP()
		}
		if !getLimiter(ip).Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
			return
		}
		c.Next()
	}
}

func (a *Authenticator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c.Request)
		if token == "" {
			abortUnauthorized(c, "missing bearer token")
			return
		}

		authenticated, username, err := a.authenticate(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service unavailable"})
			return
		}
		if !authenticated {
			abortUnauthorized(c, "invalid token")
			return
		}

		c.Set("username", username)
		c.Set("token", token)
		c.Next()
	}
}

// authenticate hashes the token, checks the shared TTL cache, and falls back
// to a live TokenReview when the cache entry is absent or expired. The result
// is written back to the cache before returning.
func (a *Authenticator) authenticate(ctx context.Context, token string) (bool, string, error) {
	key := hashToken(token)
	now := a.clock.Now()
	if cached, ok := a.cache.Get(key, now); ok {
		return cached.authenticated, cached.username, nil
	}
	authenticated, username, err := a.reviewToken(ctx, token)
	if err != nil {
		return false, "", err
	}
	ttl := a.config.NegativeCacheTTL
	if authenticated {
		ttl = a.config.CacheTTL
	}
	a.cache.Set(key, cacheEntry{
		authenticated: authenticated,
		username:      username,
		expiresAt:     now.Add(ttl),
	})
	return authenticated, username, nil
}

// ValidateToken performs a live TokenReview against the Kubernetes API,
// intentionally bypassing the shared cache so that WebSocket re-checks always
// reflect the current revocation state of the token. The result is NOT written
// back to the shared cache to avoid extending a revoked session.
func (a *Authenticator) ValidateToken(ctx context.Context, token string) (bool, error) {
	authenticated, _, err := a.reviewToken(ctx, token)
	return authenticated, err
}

func (a *Authenticator) reviewToken(ctx context.Context, token string) (bool, string, error) {
	review := &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token:     token,
			Audiences: a.config.Audiences,
		},
	}

	result, err := a.clientset.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		slog.Error("TokenReview request failed", "error", err)
		return false, "", err
	}

	return result.Status.Authenticated, result.Status.User.Username, nil
}

func abortUnauthorized(c *gin.Context, message string) {
	c.Header("WWW-Authenticate", "Bearer")
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message})
}

func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	for protocol := range strings.SplitSeq(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		protocol = strings.TrimSpace(protocol)
		if !strings.HasPrefix(protocol, webSocketTokenProtocolPrefix) {
			continue
		}
		encoded := strings.TrimPrefix(protocol, webSocketTokenProtocolPrefix)
		token, err := base64.RawURLEncoding.DecodeString(encoded)
		if err == nil {
			return string(token)
		}
	}
	return ""
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// tokenReviewCache — fixed-capacity LRU cache with TTL expiry
//
// The cache is backed by a doubly-linked list (container/list) and a map so
// both Get and Set are O(1). When the capacity is reached the
// least-recently-used entry is evicted, capping memory regardless of how many
// unique tokens an attacker injects.
// ---------------------------------------------------------------------------

type lruEntry struct {
	key   string
	value cacheEntry
}

// tokenReviewCache is a bounded, TTL-based LRU cache for TokenReview results.
type tokenReviewCache struct {
	mu       sync.Mutex
	capacity int
	clock    Clock
	items    map[string]*list.Element // key → list element
	order    *list.List               // front = most-recently-used
	stopCh   chan struct{}
}

func newTokenReviewCache(capacity int, clock Clock) *tokenReviewCache {
	c := &tokenReviewCache{
		capacity: capacity,
		clock:    clock,
		items:    make(map[string]*list.Element, capacity),
		order:    list.New(),
		stopCh:   make(chan struct{}),
	}
	go c.evictExpired()
	return c
}

// evictExpired removes TTL-expired entries every minute so that stale entries
// from legitimate users do not accumulate after their TTL elapses.
// It uses the injected Clock so the sweep is consistent with Get/Set and
// can be exercised in tests with a fake clock.
func (c *tokenReviewCache) evictExpired() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := c.clock.Now()
			c.mu.Lock()
			for key, el := range c.items {
				if !now.Before(el.Value.(*lruEntry).value.expiresAt) {
					c.order.Remove(el)
					delete(c.items, key)
				}
			}
			c.mu.Unlock()
		case <-c.stopCh:
			return
		}
	}
}

func (c *tokenReviewCache) Stop() {
	close(c.stopCh)
}

// Get returns the cached entry for key if it exists and has not expired.
// A hit moves the entry to the front of the LRU list.
func (c *tokenReviewCache) Get(key string, now time.Time) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return cacheEntry{}, false
	}
	entry := el.Value.(*lruEntry).value
	if !now.Before(entry.expiresAt) {
		// Entry has expired — evict eagerly.
		c.order.Remove(el)
		delete(c.items, key)
		return cacheEntry{}, false
	}
	// Move to front (most-recently-used).
	c.order.MoveToFront(el)
	return entry, true
}

// Set inserts or updates the entry for key. If the cache is at capacity the
// least-recently-used entry is evicted first.
func (c *tokenReviewCache) Set(key string, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		// Update existing entry and move to front.
		el.Value.(*lruEntry).value = entry
		c.order.MoveToFront(el)
		return
	}
	// Evict LRU entry if at capacity.
	if c.order.Len() >= c.capacity {
		lru := c.order.Back()
		if lru != nil {
			c.order.Remove(lru)
			delete(c.items, lru.Value.(*lruEntry).key)
		}
	}
	el := c.order.PushFront(&lruEntry{key: key, value: entry})
	c.items[key] = el
}
