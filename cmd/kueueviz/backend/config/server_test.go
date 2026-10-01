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
	"log/slog"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
}

func TestGetServerAddress(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		port   string
		want   string
	}{
		{
			name: "default port only",
			port: "8080",
			want: ":8080",
		},
		{
			name: "custom port only",
			port: "8181",
			want: ":8181",
		},
		{
			name:   "listen host and port",
			listen: "0.0.0.0:8181",
			port:   "8080",
			want:   "0.0.0.0:8181",
		},
		{
			name:   "listen overrides port",
			listen: "127.0.0.1:9090",
			port:   "8080",
			want:   "127.0.0.1:9090",
		},
		{
			name:   "listen with leading colon",
			listen: ":8181",
			want:   ":8181",
		},
		{
			name:   "listen bare port",
			listen: "8181",
			want:   ":8181",
		},
		{
			name: "empty port falls back to default",
			want: ":8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ServerConfig{Listen: tt.listen, Port: tt.port}
			if got := cfg.GetServerAddress(); got != tt.want {
				t.Fatalf("GetServerAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewServerConfigFromEnv(t *testing.T) {
	resetViper(t)

	t.Setenv("KUEUEVIZ_PORT", "8181")
	t.Setenv("KUEUEVIZ_LISTEN", "")
	t.Setenv("KUEUEVIZ_LOG_LEVEL", "debug")
	t.Setenv("KUEUEVIZ_AUTH_MODE", "Disabled")

	cfg := NewServerConfig()
	if cfg.Port != "8181" {
		t.Fatalf("Port = %q, want 8181", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if got := cfg.GetServerAddress(); got != ":8181" {
		t.Fatalf("GetServerAddress() = %q, want :8181", got)
	}
}

func TestNewServerConfigListenEnv(t *testing.T) {
	resetViper(t)

	t.Setenv("KUEUEVIZ_LISTEN", "0.0.0.0:8181")
	t.Setenv("KUEUEVIZ_PORT", "8080")

	cfg := NewServerConfig()
	if cfg.Listen != "0.0.0.0:8181" {
		t.Fatalf("Listen = %q, want 0.0.0.0:8181", cfg.Listen)
	}
	if got := cfg.GetServerAddress(); got != "0.0.0.0:8181" {
		t.Fatalf("GetServerAddress() = %q, want 0.0.0.0:8181", got)
	}
}

func TestApplyFlagsOverrideEnv(t *testing.T) {
	resetViper(t)
	t.Setenv("KUEUEVIZ_LISTEN", "0.0.0.0:8080")
	t.Setenv("KUEUEVIZ_LOG_LEVEL", "info")

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)
	if err := fs.Parse([]string{"--listen=127.0.0.1:8181", "--log-level=warn"}); err != nil {
		t.Fatalf("Parse flags: %v", err)
	}
	ApplyFlags(fs)

	cfg := NewServerConfig()
	if cfg.Listen != "127.0.0.1:8181" {
		t.Fatalf("Listen = %q, want 127.0.0.1:8181 (flag should override env)", cfg.Listen)
	}
	if cfg.LogLevel != "warn" {
		t.Fatalf("LogLevel = %q, want warn", cfg.LogLevel)
	}
	if got := cfg.GetServerAddress(); got != "127.0.0.1:8181" {
		t.Fatalf("GetServerAddress() = %q, want 127.0.0.1:8181", got)
	}
}

func TestApplyFlagsPort(t *testing.T) {
	resetViper(t)

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)
	if err := fs.Parse([]string{"--port=9090"}); err != nil {
		t.Fatalf("Parse flags: %v", err)
	}
	ApplyFlags(fs)

	cfg := NewServerConfig()
	if cfg.Port != "9090" {
		t.Fatalf("Port = %q, want 9090", cfg.Port)
	}
	if got := cfg.GetServerAddress(); got != ":9090" {
		t.Fatalf("GetServerAddress() = %q, want :9090", got)
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"nope", slog.LevelInfo},
	}
	for _, tt := range tests {
		if got := parseLogLevel(tt.in); got != tt.want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
