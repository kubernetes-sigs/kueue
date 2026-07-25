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
	utilcache "k8s.io/apimachinery/pkg/util/cache"
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

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type cacheEntry struct {
	authenticated bool
	username      string
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
	cache     *utilcache.LRUExpireCache
	clock     utilcache.Clock
}

func NewAuthenticator(clientset kubernetes.Interface, config AuthConfig) *Authenticator {
	size := config.CacheSize
	if size <= 0 {
		size = defaultCacheSize
	}
	clock := utilcache.Clock(realClock{})
	return &Authenticator{
		clientset: clientset,
		config:    config,
		cache:     utilcache.NewLRUExpireCacheWithClock(size, clock),
		clock:     clock,
	}
}

func (a *Authenticator) Stop() {
	// The utilcache.LRUExpireCache does not require stopping a goroutine.
}

// RateLimiter returns a gin middleware that enforces both a per-client-IP
// rate limit and a global rate limit. Each source IP gets its own independent
// bucket so an attacker flooding from one IP cannot drain the budget for legitimate
// clients. Requests that exceed either limit receive 429 Too Many Requests
// before they reach the authentication logic, preventing TokenReview amplification.
//
//   - perIPRate: steady-state requests per second allowed per IP.
//   - perIPBurst: maximum burst size (peak requests from a single IP at once).
//   - globalRate: total requests per second allowed globally.
//   - globalBurst: maximum global burst size.
func RateLimiter(perIPRate rate.Limit, perIPBurst int, globalRate rate.Limit, globalBurst int) gin.HandlerFunc {
	// A bounded, TTL-based cache mapping IP addresses to *rate.Limiter.
	// This prevents memory exhaustion from spoofed or highly distributed IP addresses.
	// While utilcache.LRUExpireCache is thread-safe on its own, a mutex is required
	// here to make the "Get-or-Create" operation atomic, preventing concurrent
	// requests from the same new IP from creating multiple duplicate rate limiters.
	limiters := struct {
		sync.Mutex
		cache *utilcache.LRUExpireCache
	}{
		cache: utilcache.NewLRUExpireCache(10000),
	}

	globalLimiter := rate.NewLimiter(globalRate, globalBurst)

	getLimiter := func(ip string) *rate.Limiter {
		limiters.Lock()
		defer limiters.Unlock()
		if l, ok := limiters.cache.Get(ip); ok {
			return l.(*rate.Limiter)
		}
		l := rate.NewLimiter(perIPRate, perIPBurst)
		limiters.cache.Add(ip, l, 10*time.Minute)
		return l
	}
	return func(c *gin.Context) {
		// Rely on gin's ClientIP() which automatically handles X-Forwarded-For
		// and securely validates it against trusted proxies.
		ip := c.ClientIP()

		// Check per-IP limit first so malicious IPs don't consume the global budget.
		if !getLimiter(ip).Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
			return
		}

		// Then check the global limit to protect the backend from distributed attacks.
		if !globalLimiter.Allow() {
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
	if cachedRaw, ok := a.cache.Get(key); ok {
		cached := cachedRaw.(cacheEntry)
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
	a.cache.Add(key, cacheEntry{
		authenticated: authenticated,
		username:      username,
	}, ttl)
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
