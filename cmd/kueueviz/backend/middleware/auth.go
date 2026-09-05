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
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

	ContextKeyIdentity = "identity"
	ContextKeyToken    = "token"
	ContextKeyWSError  = "ws_error"
	ContextKeyWSStatus = "ws_status"

	// Custom WebSocket close codes for granular error reporting
	wsCloseUnauthorized       = 4001
	wsCloseServiceUnavailable = 4002
	wsCloseForbidden          = 4003
)

// Identity represents the authenticated Kubernetes user.
type Identity struct {
	Username string
	Groups   []string
	UID      string
	Extra    map[string][]string
}

// IdentityFromContext returns the authenticated caller's identity as recorded by Middleware.
// Returns false if authentication is disabled or failed.
func IdentityFromContext(c *gin.Context) (Identity, bool) {
	val, exists := c.Get(ContextKeyIdentity)
	if !exists {
		return Identity{}, false
	}
	identity, ok := val.(Identity)
	return identity, ok
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type cacheEntry struct {
	authenticated bool
	identity      Identity
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
			abortWithWebSocketAwareStatus(c, http.StatusUnauthorized, "missing bearer token", wsCloseUnauthorized)
			return
		}

		authenticated, identity, err := a.authenticate(c.Request.Context(), token)
		if err != nil {
			abortWithWebSocketAwareStatus(c, http.StatusServiceUnavailable, "authentication service unavailable", wsCloseServiceUnavailable)
			return
		}
		if !authenticated {
			abortWithWebSocketAwareStatus(c, http.StatusUnauthorized, "invalid token", wsCloseUnauthorized)
			return
		}

		c.Set(ContextKeyIdentity, identity)
		c.Set(ContextKeyToken, token)
		c.Next()
	}
}

// authenticate hashes the token, checks the shared TTL cache, and falls back
// to a live TokenReview when the cache entry is absent or expired. The result
// is written back to the cache before returning.
func (a *Authenticator) authenticate(ctx context.Context, token string) (bool, Identity, error) {
	key := hashToken(token)
	if cachedRaw, ok := a.cache.Get(key); ok {
		cached := cachedRaw.(cacheEntry)
		return cached.authenticated, cached.identity, nil
	}
	authenticated, identity, err := a.reviewToken(ctx, token)
	if err != nil {
		return false, Identity{}, err
	}
	ttl := a.config.NegativeCacheTTL
	if authenticated {
		ttl = a.config.CacheTTL
	}
	a.cache.Add(key, cacheEntry{
		authenticated: authenticated,
		identity:      identity,
	}, ttl)
	return authenticated, identity, nil
}

// ValidateToken performs a live TokenReview against the Kubernetes API,
// intentionally bypassing the shared cache so that WebSocket re-checks always
// reflect the current revocation state of the token. The result is NOT written
// back to the shared cache to avoid extending a revoked session.
func (a *Authenticator) ValidateToken(ctx context.Context, token string) (bool, error) {
	authenticated, _, err := a.reviewToken(ctx, token)
	return authenticated, err
}

func (a *Authenticator) reviewToken(ctx context.Context, token string) (bool, Identity, error) {
	review := &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token:     token,
			Audiences: a.config.Audiences,
		},
	}

	result, err := a.clientset.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		slog.Error("TokenReview request failed", "error", err)
		return false, Identity{}, err
	}

	var extra map[string][]string
	if len(result.Status.User.Extra) > 0 {
		extra = make(map[string][]string, len(result.Status.User.Extra))
		for k, v := range result.Status.User.Extra {
			extra[k] = v
		}
	}

	identity := Identity{
		Username: result.Status.User.Username,
		Groups:   result.Status.User.Groups,
		UID:      result.Status.User.UID,
		Extra:    extra,
	}

	return result.Status.Authenticated, identity, nil
}

func abortWithWebSocketAwareStatus(c *gin.Context, httpStatus int, message string, wsStatus int) {
	if c.GetHeader("Upgrade") == "websocket" {
		c.Set(ContextKeyWSError, message)
		c.Set(ContextKeyWSStatus, wsStatus)
		return
	}
	if httpStatus == http.StatusUnauthorized {
		c.Header("WWW-Authenticate", "Bearer")
	}
	c.AbortWithStatusJSON(httpStatus, gin.H{"error": message})
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

// Authorizer decides whether an authenticated caller may perform an action on a resource.
type Authorizer interface {
	Authorize(ctx context.Context, identity Identity, attributes authorizationv1.ResourceAttributes) (bool, error)
}

type sarAuthorizer struct {
	client kubernetes.Interface
	config AuthConfig
	cache  *utilcache.LRUExpireCache
	clock  utilcache.Clock
}

type sarCacheKey struct {
	User        string
	Groups      string
	UID         string
	Extra       string
	Verb        string
	Group       string
	Version     string
	Resource    string
	Subresource string
	Namespace   string
	Name        string
}

func formatExtra(extra map[string][]string) string {
	if len(extra) == 0 {
		return ""
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(strings.Join(extra[k], ","))
		b.WriteString(";")
	}
	return b.String()
}

func cacheKeyForSAR(identity Identity, attributes authorizationv1.ResourceAttributes) sarCacheKey {
	return sarCacheKey{
		User:        identity.Username,
		Groups:      strings.Join(identity.Groups, ","),
		UID:         identity.UID,
		Extra:       formatExtra(identity.Extra),
		Verb:        attributes.Verb,
		Group:       attributes.Group,
		Version:     attributes.Version,
		Resource:    attributes.Resource,
		Subresource: attributes.Subresource,
		Namespace:   attributes.Namespace,
		Name:        attributes.Name,
	}
}

func NewSARAuthorizer(client kubernetes.Interface, config AuthConfig) Authorizer {
	size := config.CacheSize
	if size <= 0 {
		size = defaultCacheSize
	}
	clock := utilcache.Clock(realClock{})
	return &sarAuthorizer{
		client: client,
		config: config,
		cache:  utilcache.NewLRUExpireCacheWithClock(size, clock),
		clock:  clock,
	}
}

func (a *sarAuthorizer) Authorize(ctx context.Context, identity Identity, attributes authorizationv1.ResourceAttributes) (bool, error) {
	key := cacheKeyForSAR(identity, attributes)
	if val, ok := a.cache.Get(key); ok {
		if allowed, ok := val.(bool); ok {
			return allowed, nil
		}
	}

	var sarExtra map[string]authorizationv1.ExtraValue
	if len(identity.Extra) > 0 {
		sarExtra = make(map[string]authorizationv1.ExtraValue, len(identity.Extra))
		for k, v := range identity.Extra {
			sarExtra[k] = authorizationv1.ExtraValue(v)
		}
	}

	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:               identity.Username,
			Groups:             identity.Groups,
			UID:                identity.UID,
			Extra:              sarExtra,
			ResourceAttributes: &attributes,
		},
	}
	result, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		slog.Error("SubjectAccessReview request failed", "error", err)
		return false, err
	}

	allowed := result.Status.Allowed
	ttl := a.config.NegativeCacheTTL
	if allowed {
		ttl = a.config.CacheTTL
	}
	a.cache.Add(key, allowed, ttl)

	return allowed, nil
}

func RequireAuthorization(authorizer Authorizer, getRequests func(c *gin.Context) []authorizationv1.ResourceAttributes) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, exists := c.Get(ContextKeyWSError); exists {
			return
		}

		if authorizer == nil {
			c.Next()
			return
		}
		identity, _ := IdentityFromContext(c)
		requests := getRequests(c)

		if c.IsAborted() {
			return
		}

		for _, req := range requests {
			allowed, err := authorizer.Authorize(c.Request.Context(), identity, req)
			if err != nil {
				abortWithWebSocketAwareStatus(c, http.StatusServiceUnavailable, "authorization service unavailable", wsCloseServiceUnavailable)
				return
			}
			if !allowed {
				abortWithWebSocketAwareStatus(c, http.StatusForbidden, fmt.Sprintf("forbidden: cannot %s %s", req.Verb, req.Resource), wsCloseForbidden)
				return
			}
		}
		c.Next()
	}
}

func ResourceAccess(verb string, gvr schema.GroupVersionResource, namespace, name string) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{
		Verb:      verb,
		Group:     gvr.Group,
		Resource:  gvr.Resource,
		Namespace: namespace,
		Name:      name,
	}
}
