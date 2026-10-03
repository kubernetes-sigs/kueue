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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"kueueviz/middleware"

	_ "net/http/pprof"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	// Listen is the full listen address (host:port), e.g. "0.0.0.0:8181".
	// When empty, Port is used as ":<Port>".
	Listen string
	// Port is the listen port used when Listen is unset (backward compatible).
	Port       string
	LogLevel   string
	AuthMode   string
	AuthConfig middleware.AuthConfig
}

const (
	defaultPort     = "8080"
	defaultLogLevel = "info"
)

// RegisterFlags registers kueueviz backend CLI flags on the given FlagSet.
func RegisterFlags(fs *pflag.FlagSet) {
	fs.String("listen", "", "Address the backend listens on (host:port), e.g. 0.0.0.0:8181. Overrides --port / KUEUEVIZ_PORT when set. Env: KUEUEVIZ_LISTEN")
	fs.String("port", "", "TCP port to listen on (default 8080). Prefer --listen for host:port. Env: KUEUEVIZ_PORT")
	fs.String("log-level", "", "Log verbosity: debug, info, warn, or error (default info). Env: KUEUEVIZ_LOG_LEVEL")
}

// ParseFlags parses process arguments and applies CLI flag overrides into viper.
// Call before NewServerConfig. Unknown flags cause a non-zero exit via ExitOnError.
func ParseFlags(args []string) {
	fs := pflag.NewFlagSet(os.Args[0], pflag.ExitOnError)
	RegisterFlags(fs)
	_ = fs.Parse(args)
	ApplyFlags(fs)
}

// ApplyFlags copies non-empty CLI flag values into viper so they take precedence over env defaults.
func ApplyFlags(fs *pflag.FlagSet) {
	if v, err := fs.GetString("listen"); err == nil && v != "" {
		viper.Set("KUEUEVIZ_LISTEN", v)
	}
	if v, err := fs.GetString("port"); err == nil && v != "" {
		viper.Set("KUEUEVIZ_PORT", v)
	}
	if v, err := fs.GetString("log-level"); err == nil && v != "" {
		viper.Set("KUEUEVIZ_LOG_LEVEL", v)
	}
}

// NewServerConfig creates a new server configuration from environment variables and any
// previously applied CLI flags (see ParseFlags / ApplyFlags).
func NewServerConfig() *ServerConfig {
	viper.AutomaticEnv()
	viper.SetDefault("KUEUEVIZ_PORT", defaultPort)
	viper.SetDefault("KUEUEVIZ_LISTEN", "")
	viper.SetDefault("KUEUEVIZ_LOG_LEVEL", defaultLogLevel)
	viper.SetDefault("KUEUEVIZ_AUTH_MODE", "Disabled")
	viper.SetDefault("KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL", "60s")
	viper.SetDefault("KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL", "5s")

	var audiences []string
	if raw := viper.GetString("KUEUEVIZ_AUTH_TOKEN_REVIEW_AUDIENCES"); raw != "" {
		for a := range strings.SplitSeq(raw, ",") {
			if a = strings.TrimSpace(a); a != "" {
				audiences = append(audiences, a)
			}
		}
	}

	cacheTTL := parseDurationWithDefault(
		viper.GetString("KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL"), 60*time.Second, "KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL",
	)
	negativeCacheTTL := parseDurationWithDefault(
		viper.GetString("KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL"), 5*time.Second, "KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL",
	)

	return &ServerConfig{
		Listen:   strings.TrimSpace(viper.GetString("KUEUEVIZ_LISTEN")),
		Port:     viper.GetString("KUEUEVIZ_PORT"),
		LogLevel: viper.GetString("KUEUEVIZ_LOG_LEVEL"),
		AuthMode: viper.GetString("KUEUEVIZ_AUTH_MODE"),
		AuthConfig: middleware.AuthConfig{
			Audiences:        audiences,
			CacheTTL:         cacheTTL,
			NegativeCacheTTL: negativeCacheTTL,
		},
	}
}

// SetupLogging configures the default slog logger from the configured log level.
func SetupLogging(level string) {
	lvl := parseLogLevel(level)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(handler))
}

// SetupPprof starts the pprof server in development mode
func SetupPprof() {
	if gin.Mode() != gin.ReleaseMode {
		go func() {
			slog.Info("Starting pprof server on localhost:6060")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				slog.Error("Error starting pprof server", "error", err)
			}
		}()
	}
}

// SetupGinEngine creates and configures the Gin engine with base middleware.
func SetupGinEngine() (*gin.Engine, error) {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	corsMiddleware, err := middleware.SetupCORS()
	if err != nil {
		return nil, fmt.Errorf("error setting up CORS: %v", err)
	}
	r.Use(corsMiddleware)

	if err := r.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("error setting trusted proxies: %v", err)
	}

	return r, nil
}

// GetServerAddress returns the listen address for the HTTP server.
// Prefer Listen (host:port) when set; otherwise use ":<Port>".
func (c *ServerConfig) GetServerAddress() string {
	if c.Listen != "" {
		return normalizeListenAddress(c.Listen)
	}
	port := c.Port
	if port == "" {
		port = defaultPort
	}
	return net.JoinHostPort("", port)
}

func normalizeListenAddress(listen string) string {
	// Allow bare port ("8181") or ":8181" as well as "host:port".
	if !strings.Contains(listen, ":") {
		return net.JoinHostPort("", listen)
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		// net.SplitHostPort fails for ":8080" on some inputs; try with empty host.
		if strings.HasPrefix(listen, ":") {
			return listen
		}
		return listen
	}
	return net.JoinHostPort(host, port)
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseDurationWithDefault(raw string, fallback time.Duration, envName string) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("Invalid duration, using default", "env", envName, "value", raw, "default", fallback)
		return fallback
	}
	return d
}
