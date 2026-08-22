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
	"testing"

	"github.com/spf13/viper"
)

func TestGetServerAddress(t *testing.T) {
	testCases := map[string]struct {
		authMode string
		port     string
		want     string
	}{
		"default auth mode only listens on loopback": {
			port: "8080",
			want: "127.0.0.1:8080",
		},
		"disabled auth only listens on loopback": {
			authMode: "Disabled",
			port:     "8080",
			want:     "127.0.0.1:8080",
		},
		"token review listens on all interfaces": {
			authMode: "TokenReview",
			port:     "8181",
			want:     ":8181",
		},
		"unknown auth mode fails closed on loopback": {
			authMode: "unknown",
			port:     "8080",
			want:     "127.0.0.1:8080",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			t.Setenv("KUEUEVIZ_AUTH_MODE", tc.authMode)
			t.Setenv("KUEUEVIZ_PORT", tc.port)

			if got := NewServerConfig().GetServerAddress(); got != tc.want {
				t.Fatalf("GetServerAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}
