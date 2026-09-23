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

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	certutil "k8s.io/client-go/util/cert"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestMetricsServerOptions(t *testing.T) {
	cases := map[string]struct {
		args       []string
		address    string
		wantFilter bool
		wantError  bool
	}{
		"default authentication":           {address: ":8443", wantFilter: true},
		"explicit authentication":          {args: []string{"--metrics-authentication=true"}, address: ":8443", wantFilter: true},
		"authenticated hostname unchanged": {address: "localhost:8443", wantFilter: true},
		"authenticated empty unchanged":    {wantFilter: true},
		"disabled authenticated metrics":   {address: "0", wantFilter: true},
		"disabled unauthenticated metrics": {args: []string{"--metrics-authentication=false"}, address: "0"},
		"IPv4 loopback":                    {args: []string{"--metrics-authentication=false"}, address: "127.0.0.1:8443"},
		"IPv4 loopback subnet":             {args: []string{"--metrics-authentication=false"}, address: "127.23.45.67:8443"},
		"IPv6 loopback":                    {args: []string{"--metrics-authentication=false"}, address: "[::1]:8443"},
		"expanded IPv6 loopback":           {args: []string{"--metrics-authentication=false"}, address: "[0:0:0:0:0:0:0:1]:8443"},
		"mapped IPv4 loopback":             {args: []string{"--metrics-authentication=false"}, address: "[::ffff:127.0.0.1]:8443"},
		"ephemeral loopback port":          {args: []string{"--metrics-authentication=false"}, address: "127.0.0.1:0"},
		"highest port":                     {args: []string{"--metrics-authentication=false"}, address: "127.0.0.1:65535"},
	}
	for name, address := range map[string]string{
		"empty": "", "unspecified host": ":8443", "IPv4 wildcard": "0.0.0.0:8443",
		"IPv6 wildcard": "[::]:8443", "IPv4 non-loopback": "192.0.2.1:8443",
		"IPv6 non-loopback": "[2001:db8::1]:8443", "mapped non-loopback": "[::ffff:192.0.2.1]:8443",
		"localhost hostname": "localhost:8443", "DNS hostname": "metrics.example.test:8443",
		"missing port": "127.0.0.1", "empty port": "127.0.0.1:", "named port": "127.0.0.1:https",
		"negative port": "127.0.0.1:-1", "out of range port": "127.0.0.1:65536",
		"unbracketed IPv6": "::1:8443", "extra port": "127.0.0.1:8443:8443",
		"URL": "https://127.0.0.1:8443", "whitespace": " 127.0.0.1:8443",
		"invalid IP": "127.0.0.999:8443", "short IPv4": "127.1:8443",
	} {
		cases[name] = struct {
			args       []string
			address    string
			wantFilter bool
			wantError  bool
		}{
			args: []string{"--metrics-authentication=false"}, address: address, wantError: true,
		}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			o := parseMetricsOptions(t, tc.args)
			options, err := o.serverOptions(tc.address)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "--metrics-authentication=false") || !strings.Contains(err.Error(), "metrics.bindAddress") {
					t.Fatalf("expected actionable configuration error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !options.SecureServing {
				t.Error("HTTPS must remain enabled")
			}
			if options.BindAddress != tc.address {
				t.Errorf("bind address = %q, want %q", options.BindAddress, tc.address)
			}
			if (options.FilterProvider != nil) != tc.wantFilter {
				t.Errorf("filter present = %t, want %t", options.FilterProvider != nil, tc.wantFilter)
			}
			if tc.address == "0" {
				server, err := metricsserver.NewServer(options, nil, nil)
				if err != nil || server != nil {
					t.Fatalf("disabled metrics created server %v, error %v", server, err)
				}
			}
		})
	}
}

// Run the actual entry point in a subprocess to verify rejection before certificate or Kubernetes setup.
func TestMetricsManagerStartup(t *testing.T) {
	if configFile := os.Getenv("KUEUE_TEST_METRICS_CONFIG"); configFile != "" {
		flag.CommandLine = flag.NewFlagSet("manager", flag.ExitOnError)
		os.Args = []string{"manager", "--config=" + configFile, "--metrics-authentication=false"}
		main()
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, address := range map[string]string{
		"default wildcard": "", "IPv4 wildcard": "0.0.0.0:8443", "IPv6 wildcard": "[::]:8443",
		"non-loopback": "192.0.2.1:8443", "hostname": "localhost:8443", "malformed": "127.0.0.1",
	} {
		t.Run(name, func(t *testing.T) {
			configFile := filepath.Join(t.TempDir(), "config.yaml")
			config := "apiVersion: config.kueue.x-k8s.io/v1beta2\nkind: Configuration\ninternalCertManagement:\n  enable: false\nmetrics:\n  bindAddress: " + `"` + address + `"` + "\n"
			if err := os.WriteFile(configFile, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestMetricsManagerStartup$")
			cmd.Env = append(os.Environ(), "KUEUE_TEST_METRICS_CONFIG="+configFile)
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("expected startup exit 1, got %v: %s", err, output)
			}
			if !strings.Contains(string(output), "Unable to configure metrics server") || !strings.Contains(string(output), "--metrics-authentication=false") {
				t.Fatalf("startup did not reject unsafe metrics configuration: %s", output)
			}
		})
	}
}

func TestMetricsHTTPSAuthentication(t *testing.T) {
	certPEM, keyPEM := metricsTestCertificate(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	cases := map[string]struct {
		args               []string
		address            string
		wantAuthentication bool
	}{
		"default":       {address: "127.0.0.1:0", wantAuthentication: true},
		"explicit true": {args: []string{"--metrics-authentication=true"}, address: "127.0.0.1:0", wantAuthentication: true},
		"false IPv4":    {args: []string{"--metrics-authentication=false"}, address: "127.0.0.1:0"},
		"false IPv6":    {args: []string{"--metrics-authentication=false"}, address: "[::1]:0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var tokenReviews, accessReviews atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/apis/authentication.k8s.io/v1/tokenreviews":
					tokenReviews.Add(1)
					var review authenticationv1.TokenReview
					if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					review.TypeMeta = metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}
					review.Status = authenticationv1.TokenReviewStatus{Authenticated: review.Spec.Token != "invalid", User: authenticationv1.UserInfo{Username: review.Spec.Token}}
					if err := json.NewEncoder(w).Encode(review); err != nil {
						t.Error(err)
					}
				case "/apis/authorization.k8s.io/v1/subjectaccessreviews":
					accessReviews.Add(1)
					var review authorizationv1.SubjectAccessReview
					if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					review.TypeMeta = metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}
					review.Status.Allowed = review.Spec.User == "allowed"
					if err := json.NewEncoder(w).Encode(review); err != nil {
						t.Error(err)
					}
				default:
					t.Errorf("unexpected Kubernetes request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()
			o := parseMetricsOptions(t, tc.args)
			options, err := o.serverOptions(tc.address)
			if err != nil {
				t.Fatal(err)
			}
			certDir := t.TempDir()
			writeMetricsCertificate(t, certDir, certPEM, keyPEM)
			watcher, err := setupMetricsCertWatcher(&options, certDir)
			if err != nil {
				t.Fatal(err)
			}
			startMetricsRunnable(t, watcher.Start)
			url := startMetricsTestServer(t, options, &rest.Config{Host: api.URL}, api.Client())
			client := metricsTestClient(t, roots, "metrics.example.test")
			for _, token := range []string{"", "invalid", "denied", "allowed"} {
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/metrics", nil)
				if err != nil {
					t.Fatal(err)
				}
				if token != "" {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				wantStatus := http.StatusOK
				if tc.wantAuthentication {
					switch token {
					case "":
						wantStatus = http.StatusUnauthorized
					case "invalid":
						// The pinned filter reports bearer-token authentication errors as 500.
						wantStatus = http.StatusInternalServerError
					case "denied":
						wantStatus = http.StatusForbidden
					}
				}
				if response.StatusCode != wantStatus {
					t.Fatalf("token %q: status = %d, want %d: %s", token, response.StatusCode, wantStatus, body)
				}
				if wantStatus == http.StatusOK && !strings.Contains(string(body), "# HELP") {
					t.Fatalf("missing Prometheus metrics: %s", body)
				}
			}
			if tc.wantAuthentication {
				if tokenReviews.Load() != 3 || accessReviews.Load() != 2 {
					t.Errorf("review counts = (%d, %d), want (3, 2)", tokenReviews.Load(), accessReviews.Load())
				}
			} else if tokenReviews.Load() != 0 || accessReviews.Load() != 0 {
				t.Errorf("unauthenticated scraping called review APIs: (%d, %d)", tokenReviews.Load(), accessReviews.Load())
			}
			for name, tlsClient := range map[string]*http.Client{
				"untrusted CA": metricsTestClient(t, x509.NewCertPool(), "metrics.example.test"),
				"DNS certificate does not match loopback IP": metricsTestClient(t, roots, ""),
			} {
				t.Run(name, func(t *testing.T) {
					response, err := tlsClient.Get(url + "/metrics")
					if response != nil {
						response.Body.Close()
					}
					if _, ok := errors.AsType[*tls.CertificateVerificationError](err); !ok {
						t.Fatalf("expected TLS verification failure, got %v", err)
					}
				})
			}
			plainClient := &http.Client{Timeout: 5 * time.Second}
			response, err := plainClient.Get(strings.Replace(url, "https://", "http://", 1) + "/metrics")
			if response != nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					t.Fatal("metrics served over plaintext HTTP")
				}
			} else if err == nil {
				t.Fatal("expected plaintext rejection")
			}
		})
	}
}

func TestMetricsCertificateRotation(t *testing.T) {
	firstCert, firstKey := metricsTestCertificate(t)
	secondCert, secondKey := metricsTestCertificate(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(firstCert)
	roots.AppendCertsFromPEM(secondCert)
	o := parseMetricsOptions(t, []string{"--metrics-authentication=false"})
	options, err := o.serverOptions("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The watcher must preserve TLS options already configured by the manager.
	options.TLSOpts = []func(*tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS13 }}
	certDir := t.TempDir()
	writeMetricsCertificate(t, certDir, firstCert, firstKey)
	watcher, err := setupMetricsCertWatcher(&options, certDir)
	if err != nil {
		t.Fatal(err)
	}
	startMetricsRunnable(t, watcher.Start)
	url := startMetricsTestServer(t, options, nil, nil)
	client := metricsTestClient(t, roots, "metrics.example.test")
	scrape := func() *http.Response {
		t.Helper()
		response, err := client.Get(url + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("scrape returned %d", response.StatusCode)
		}
		if response.TLS.Version != tls.VersionTLS13 {
			t.Fatalf("TLS options lost: version %d", response.TLS.Version)
		}
		return response
	}
	initial := scrape().TLS.PeerCertificates[0].SerialNumber
	writeMetricsCertificate(t, certDir, secondCert, secondKey)
	if err := wait.PollUntilContextTimeout(t.Context(), 50*time.Millisecond, 15*time.Second, true, func(context.Context) (bool, error) {
		return scrape().TLS.PeerCertificates[0].SerialNumber.Cmp(initial) != 0, nil
	}); err != nil {
		t.Fatalf("serving certificate was not rotated: %v", err)
	}
}

func TestMetricsCertificateLoadingError(t *testing.T) {
	for name, content := range map[string]string{"missing": "", "malformed": "not a certificate"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if content != "" {
				writeMetricsCertificate(t, dir, []byte(content), []byte(content))
			}
			options := metricsserver.Options{SecureServing: true}
			if _, err := setupMetricsCertWatcher(&options, dir); err == nil {
				t.Fatal("expected error loading serving certificate")
			}
		})
	}
}

func parseMetricsOptions(t *testing.T, args []string) metricsOptions {
	t.Helper()
	var o metricsOptions
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	o.bindFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return o
}

func metricsTestCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	certPEM, keyPEM, err := certutil.GenerateSelfSignedCertKey("metrics.example.test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM
}

func writeMetricsCertificate(t *testing.T, dir string, certPEM, keyPEM []byte) {
	t.Helper()
	for name, content := range map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func metricsTestClient(t *testing.T, roots *x509.CertPool, serverName string) *http.Client {
	t.Helper()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func startMetricsRunnable(t *testing.T, start func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("metrics runnable failed: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("metrics runnable did not stop")
		}
	})
}

func startMetricsTestServer(t *testing.T, options metricsserver.Options, cfg *rest.Config, client *http.Client) string {
	t.Helper()
	server, err := metricsserver.NewServer(options, cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	startMetricsRunnable(t, server.Start)
	address := server.(interface{ GetBindAddr() string })
	if err := wait.PollUntilContextTimeout(t.Context(), 20*time.Millisecond, 10*time.Second, true, func(context.Context) (bool, error) {
		return address.GetBindAddr() != "", nil
	}); err != nil {
		t.Fatalf("metrics server did not bind: %v", err)
	}
	return "https://" + address.GetBindAddr()
}
