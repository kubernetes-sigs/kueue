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
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// WebSocketBaseProtocol is the mandatory subprotocol used by frontend and backend.
	WebSocketBaseProtocol = "kueueviz.v1"

	webSocketTokenProtocolPrefix = "kueueviz.auth."
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
}

type Authenticator struct {
	clientset kubernetes.Interface
	config    AuthConfig
	cache     *tokenReviewCache
	clock     Clock
}

func NewAuthenticator(clientset kubernetes.Interface, config AuthConfig) *Authenticator {
	return &Authenticator{
		clientset: clientset,
		config:    config,
		cache:     newTokenReviewCache(),
		clock:     realClock{},
	}
}

func (a *Authenticator) Stop() {
	a.cache.Stop()
}

func (a *Authenticator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c.Request)
		if token == "" {
			abortUnauthorized(c, "missing bearer token")
			return
		}

		key := hashToken(token)

		now := a.clock.Now()
		if cached, ok := a.cache.Get(key, now); ok {
			if cached.authenticated {
				c.Set("username", cached.username)
				c.Next()
				return
			}
			abortUnauthorized(c, "invalid token")
			return
		}

		authenticated, username, err := a.reviewToken(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service unavailable"})
			return
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

		if !authenticated {
			abortUnauthorized(c, "invalid token")
			return
		}

		c.Set("username", username)
		c.Next()
	}
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

// tokenReviewCache is a simple TTL-based cache for TokenReview results.
type tokenReviewCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	stopCh  chan struct{}
}

func newTokenReviewCache() *tokenReviewCache {
	c := &tokenReviewCache{
		entries: make(map[string]cacheEntry),
		stopCh:  make(chan struct{}),
	}
	go c.evictExpired()
	return c
}

func (c *tokenReviewCache) evictExpired() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			c.mu.Lock()
			for key, entry := range c.entries {
				if !now.Before(entry.expiresAt) {
					delete(c.entries, key)
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

func (c *tokenReviewCache) Get(key string, now time.Time) (cacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return cacheEntry{}, false
	}
	if now.Before(entry.expiresAt) {
		return entry, true
	}

	c.mu.Lock()
	entry, ok = c.entries[key]
	if ok && !now.Before(entry.expiresAt) {
		delete(c.entries, key)
	}
	c.mu.Unlock()
	return cacheEntry{}, false
}

func (c *tokenReviewCache) Set(key string, entry cacheEntry) {
	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
}
