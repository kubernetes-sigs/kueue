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

	tests := []struct {
		name           string
		origin         string
		host           string
		allowedOrigins string
		ginMode        string
		expected       bool
	}{
		{
			name:     "empty origin is allowed",
			origin:   "",
			host:     "example.com",
			expected: true,
		},
		{
			name:     "same host is allowed",
			origin:   "http://example.com:8080",
			host:     "example.com:8080",
			expected: true,
		},
		{
			name:     "different host is denied",
			origin:   "http://malicious.com",
			host:     "example.com",
			expected: false,
		},
		{
			name:     "localhost cross-port in dev mode is allowed",
			origin:   "http://localhost:3000",
			host:     "localhost:8080",
			ginMode:  gin.DebugMode,
			expected: true,
		},
		{
			name:     "localhost cross-port in release mode is denied without config",
			origin:   "http://localhost:3000",
			host:     "localhost:8080",
			ginMode:  gin.ReleaseMode,
			expected: false,
		},
		{
			name:           "localhost cross-port in release mode is allowed with config",
			origin:         "http://localhost:3000",
			host:           "localhost:8080",
			allowedOrigins: "http://localhost:3000",
			ginMode:        gin.ReleaseMode,
			expected:       true,
		},
		{
			name:           "allowed origin from config is allowed",
			origin:         "https://allowed.com",
			host:           "example.com",
			allowedOrigins: "https://allowed.com",
			expected:       true,
		},
		{
			name:           "unlisted origin with config is denied",
			origin:         "https://not-allowed.com",
			host:           "example.com",
			allowedOrigins: "https://allowed.com",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
	tests := []struct {
		name     string
		origin   string
		expected string
	}{
		{
			name:     "http with port 80",
			origin:   "http://example.com:80",
			expected: "http://example.com",
		},
		{
			name:     "https with port 443",
			origin:   "https://example.com:443",
			expected: "https://example.com",
		},
		{
			name:     "custom port",
			origin:   "http://example.com:8080",
			expected: "http://example.com:8080",
		},
		{
			name:     "mixed case",
			origin:   "HTTP://ExAmPle.COM:80",
			expected: "http://example.com",
		},
		{
			name:     "invalid url fallback",
			origin:   "://invalid",
			expected: "://invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := canonicalizeOrigin(tt.origin)
			if result != tt.expected {
				t.Errorf("canonicalizeOrigin() = %v, want %v", result, tt.expected)
			}
		})
	}
}
