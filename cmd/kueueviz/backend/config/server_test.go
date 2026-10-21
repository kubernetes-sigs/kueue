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

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/viper"
)

func TestNewServerConfigAuthMode(t *testing.T) {
	tests := map[string]struct {
		authMode string
		wantMode string
		wantErr  string
	}{
		"default auth mode is disabled": {
			wantMode: AuthModeDisabled,
		},
		"disabled auth mode is accepted": {
			authMode: AuthModeDisabled,
			wantMode: AuthModeDisabled,
		},
		"token review auth mode is accepted": {
			authMode: AuthModeTokenReview,
			wantMode: AuthModeTokenReview,
		},
		"unknown auth mode is rejected": {
			authMode: "tokenreview",
			wantErr:  "unsupported KUEUEVIZ_AUTH_MODE",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			t.Setenv("KUEUEVIZ_AUTH_MODE", tt.authMode)

			got, err := NewServerConfig()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("NewServerConfig() error = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewServerConfig() error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewServerConfig() unexpected error: %v", err)
			}
			if got.AuthMode != tt.wantMode {
				t.Errorf("AuthMode = %q, want %q", got.AuthMode, tt.wantMode)
			}
		})
	}
}

func TestNewServerConfigTokenReviewSettings(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("KUEUEVIZ_AUTH_MODE", AuthModeTokenReview)
	t.Setenv("KUEUEVIZ_AUTH_TOKEN_REVIEW_AUDIENCES", "kueueviz, api")
	t.Setenv("KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL", "2m")
	t.Setenv("KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL", "10s")

	got, err := NewServerConfig()
	if err != nil {
		t.Fatalf("NewServerConfig() unexpected error: %v", err)
	}

	if got.AuthConfig.CacheTTL != 2*time.Minute {
		t.Errorf("CacheTTL = %v, want %v", got.AuthConfig.CacheTTL, 2*time.Minute)
	}
	if got.AuthConfig.NegativeCacheTTL != 10*time.Second {
		t.Errorf("NegativeCacheTTL = %v, want %v", got.AuthConfig.NegativeCacheTTL, 10*time.Second)
	}
	wantAudiences := []string{"kueueviz", "api"}
	if diff := cmp.Diff(wantAudiences, got.AuthConfig.Audiences); diff != "" {
		t.Errorf("Audiences mismatch (-want,+got):\n%s", diff)
	}
}
