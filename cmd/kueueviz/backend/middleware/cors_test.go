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
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func TestValidateWebSocketOrigin(t *testing.T) {
	// Save and restore gin mode
	originalMode := gin.Mode()
	t.Cleanup(func() {
		gin.SetMode(originalMode)
	})

	tests := map[string]struct {
		origin         string
		host           string
		allowedOrigins string
		ginMode        string
		expected       bool
	}{
		"empty origin is allowed": {
			origin:   "",
			host:     "example.com",
			expected: true,
		},
		"same host is allowed": {
			origin:   "http://example.com:8080",
			host:     "example.com:8080",
			expected: true,
		},
		"different host is denied": {
			origin:   "http://malicious.com",
			host:     "example.com",
			expected: false,
		},
		"localhost cross-port in dev mode is allowed": {
			origin:   "http://localhost:3000",
			host:     "localhost:8080",
			ginMode:  gin.DebugMode,
			expected: true,
		},
		"localhost cross-port in release mode is denied without config": {
			origin:   "http://localhost:3000",
			host:     "localhost:8080",
			ginMode:  gin.ReleaseMode,
			expected: false,
		},
		"localhost cross-port in release mode is allowed with config": {
			origin:         "http://localhost:3000",
			host:           "localhost:8080",
			allowedOrigins: "http://localhost:3000",
			ginMode:        gin.ReleaseMode,
			expected:       true,
		},
		"allowed origin from config is allowed": {
			origin:         "https://allowed.com",
			host:           "example.com",
			allowedOrigins: "https://allowed.com",
			expected:       true,
		},
		"unlisted origin with config is denied": {
			origin:         "https://not-allowed.com",
			host:           "example.com",
			allowedOrigins: "https://allowed.com",
			expected:       false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.ginMode != "" {
				gin.SetMode(tt.ginMode)
			} else {
				gin.SetMode(gin.DebugMode)
			}

			if tt.allowedOrigins != "" {
				viper.Set("KUEUEVIZ_ALLOWED_ORIGINS", tt.allowedOrigins)
			} else {
				viper.Set("KUEUEVIZ_ALLOWED_ORIGINS", "")
			}

			result := ValidateWebSocketOrigin(tt.origin, tt.host)
			if result != tt.expected {
				t.Errorf("ValidateWebSocketOrigin() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestCanonicalizeOrigin(t *testing.T) {
	tests := map[string]struct {
		origin   string
		expected string
	}{
		"http with port 80": {
			origin:   "http://example.com:80",
			expected: "http://example.com",
		},
		"https with port 443": {
			origin:   "https://example.com:443",
			expected: "https://example.com",
		},
		"custom port": {
			origin:   "http://example.com:8080",
			expected: "http://example.com:8080",
		},
		"mixed case": {
			origin:   "HTTP://ExAmPle.COM:80",
			expected: "http://example.com",
		},
		"invalid url fallback": {
			origin:   "://invalid",
			expected: "://invalid",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := canonicalizeOrigin(tt.origin)
			if result != tt.expected {
				t.Errorf("canonicalizeOrigin() = %v, want %v", result, tt.expected)
			}
		})
	}
}
