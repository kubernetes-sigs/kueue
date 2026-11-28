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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/cert"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

type metricsTestCA struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
}

func newMetricsTestCA(t *testing.T) *metricsTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "metrics-test-client-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &metricsTestCA{certificate: certificate, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (c *metricsTestCA) client(t *testing.T, username string, usage x509.ExtKeyUsage, expired bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notAfter := time.Now().Add(30 * time.Minute)
	if expired {
		notAfter = time.Now().Add(-time.Minute)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: username, Organization: []string{"metrics-team"}},
		NotBefore: time.Now().Add(-30 * time.Minute), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, c.certificate, key.Public(), c.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, c.certificate.Raw}, PrivateKey: key}
}

type metricsReviewRecorder struct {
	mu                      sync.Mutex
	tokens                  int
	sars                    []authorizationv1.SubjectAccessReviewSpec
	denyTokenReview         bool
	denySubjectAccessReview bool
}

func (r *metricsReviewRecorder) serve(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	r.mu.Lock()
	defer r.mu.Unlock()
	switch request.URL.Path {
	case "/apis/authentication.k8s.io/v1/tokenreviews":
		var review authenticationv1.TokenReview
		if err := json.NewDecoder(request.Body).Decode(&review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.tokens++
		if r.denyTokenReview {
			http.Error(w, "TokenReview creation forbidden", http.StatusForbidden)
			return
		}
		review.TypeMeta = metav1.TypeMeta{Kind: "TokenReview", APIVersion: "authentication.k8s.io/v1"}
		if review.Spec.Token == "valid-token" {
			review.Status.Authenticated = true
			review.Status.User = authenticationv1.UserInfo{Username: "token-reader"}
		}
		_ = json.NewEncoder(w).Encode(review)
	case "/apis/authorization.k8s.io/v1/subjectaccessreviews":
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(request.Body).Decode(&review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.sars = append(r.sars, review.Spec)
		if r.denySubjectAccessReview {
			http.Error(w, "SubjectAccessReview creation forbidden", http.StatusForbidden)
			return
		}
		review.TypeMeta = metav1.TypeMeta{Kind: "SubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}
		review.Status.Allowed = review.Spec.User == "cert-reader" || review.Spec.User == "token-reader"
		_ = json.NewEncoder(w).Encode(review)
	default:
		http.NotFound(w, request)
	}
}

type metricsAuthFixture struct {
	ctx     context.Context
	server  *httptest.Server
	reviews *metricsReviewRecorder
	watcher *metricsClientCAWatcher
	caPath  string
}

func newMetricsAuthFixture(t *testing.T, ca *metricsTestCA) *metricsAuthFixture {
	t.Helper()
	features.SetFeatureGateDuringTest(t, features.MetricsAccessControl, true)
	path := filepath.Join(t.TempDir(), "client-ca.crt")
	if err := os.WriteFile(path, ca.pem, 0600); err != nil {
		t.Fatal(err)
	}
	options, runnable, err := MetricsServerOptions(logr.Discard(), configapi.ControllerMetrics{
		BindAddress: ":8443", ClientCAFile: new(path), AccessControlMode: new(configapi.MetricsAccessControlModeClientCertificateOrBearer),
	})
	if err != nil {
		t.Fatal(err)
	}
	watcher, ok := runnable.(*metricsClientCAWatcher)
	if !ok || watcher.NeedLeaderElection() {
		t.Fatal("client CA reload must run independently of leader election")
	}
	reviews := &metricsReviewRecorder{}
	api := httptest.NewServer(http.HandlerFunc(reviews.serve))
	t.Cleanup(api.Close)
	provider, err := options.FilterProvider(&rest.Config{
		Host: api.URL, ContentType: "application/json", AcceptContentTypes: "application/json",
	}, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := provider(logr.Discard(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "test_metric 1\n")
	}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	servingCert, servingKey, err := cert.GenerateSelfSignedCertKey("localhost", []net.IP{{127, 0, 0, 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPair, err := tls.X509KeyPair(servingCert, servingKey)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{keyPair}}
	for _, option := range options.TLSOpts {
		option(server.TLS)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return &metricsAuthFixture{ctx: t.Context(), server: server, reviews: reviews, watcher: watcher, caPath: path}
}

func (f *metricsAuthFixture) consumer(t *testing.T, certificate *tls.Certificate) *http.Client {
	t.Helper()
	transport := f.server.Client().Transport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	if certificate != nil {
		// Force presentation even for an untrusted issuer; otherwise Go may omit
		// the certificate when the server advertises a different acceptable CA.
		transport.TLSClientConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return certificate, nil
		}
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func (f *metricsAuthFixture) scrape(consumer *http.Client, token string) (int, error) {
	request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, f.server.URL+"/metrics", nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := consumer.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	return response.StatusCode, err
}

func TestMetricsCertificateAuthentication(t *testing.T) {
	cases := map[string]struct {
		username                string
		token                   string
		withCert                bool
		untrusted               bool
		expired                 bool
		usage                   x509.ExtKeyUsage
		wantCode                int
		wantTLS                 bool
		wantToken               int
		wantUser                string
		denyTokenReview         bool
		denySubjectAccessReview bool
	}{
		"invalid bearer rejected":       {token: "invalid-token", wantCode: http.StatusInternalServerError, wantToken: 1},
		"anonymous rejected":            {wantCode: http.StatusUnauthorized},
		"bearer only remains supported": {token: "valid-token", wantCode: http.StatusOK, wantToken: 1, wantUser: "token-reader"},
		"certificate without token":     {username: "cert-reader", withCert: true, usage: x509.ExtKeyUsageClientAuth, wantCode: http.StatusOK, wantUser: "cert-reader"},
		"certificate wins over another authorized identity": {
			username: "cert-reader", token: "valid-token", withCert: true, usage: x509.ExtKeyUsageClientAuth, wantCode: http.StatusOK, wantUser: "cert-reader",
		},
		"certificate does not depend on TokenReview permission": {
			username: "cert-reader", withCert: true, usage: x509.ExtKeyUsageClientAuth, denyTokenReview: true, wantCode: http.StatusOK, wantUser: "cert-reader",
		},
		"certificate cannot bypass SubjectAccessReview failure": {
			username: "cert-reader", token: "valid-token", withCert: true, usage: x509.ExtKeyUsageClientAuth, denySubjectAccessReview: true,
			wantCode: http.StatusInternalServerError, wantUser: "cert-reader",
		},
		"bearer fails closed when TokenReview is forbidden": {
			token: "valid-token", denyTokenReview: true, wantCode: http.StatusInternalServerError, wantToken: 1,
		},
		"unbound certificate denied": {username: "unbound", withCert: true, usage: x509.ExtKeyUsageClientAuth, wantCode: http.StatusForbidden, wantUser: "unbound"},
		"denied certificate cannot retry as token": {
			username: "unbound",
			token:    "valid-token",
			withCert: true,
			usage:    x509.ExtKeyUsageClientAuth,
			wantCode: http.StatusForbidden,
			wantUser: "unbound",
		},
		"certificate wins over invalid token": {
			username: "cert-reader",
			token:    "invalid-token",
			withCert: true,
			usage:    x509.ExtKeyUsageClientAuth,
			wantCode: http.StatusOK,
			wantUser: "cert-reader",
		},
		"empty certificate identity cannot fall back": {token: "valid-token", withCert: true, usage: x509.ExtKeyUsageClientAuth, wantCode: http.StatusUnauthorized},
		"untrusted certificate cannot fall back":      {username: "cert-reader", token: "valid-token", withCert: true, untrusted: true, usage: x509.ExtKeyUsageClientAuth, wantTLS: true},
		"expired certificate rejected":                {username: "cert-reader", withCert: true, expired: true, usage: x509.ExtKeyUsageClientAuth, wantTLS: true},
		"server-only certificate rejected":            {username: "cert-reader", withCert: true, usage: x509.ExtKeyUsageServerAuth, wantTLS: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ca := newMetricsTestCA(t)
			fixture := newMetricsAuthFixture(t, ca)
			fixture.reviews.mu.Lock()
			fixture.reviews.denyTokenReview = tc.denyTokenReview
			fixture.reviews.denySubjectAccessReview = tc.denySubjectAccessReview
			fixture.reviews.mu.Unlock()
			var certificate *tls.Certificate
			if tc.withCert {
				issuer := ca
				if tc.untrusted {
					issuer = newMetricsTestCA(t)
				}
				leaf := issuer.client(t, tc.username, tc.usage, tc.expired)
				certificate = &leaf
			}
			code, err := fixture.scrape(fixture.consumer(t, certificate), tc.token)
			if tc.wantTLS {
				if err == nil {
					t.Fatalf("expected TLS credential rejection, got HTTP %d", code)
				}
			} else if err != nil || code != tc.wantCode {
				t.Fatalf("scrape returned HTTP %d, %v; want HTTP %d", code, err, tc.wantCode)
			}
			fixture.reviews.mu.Lock()
			defer fixture.reviews.mu.Unlock()
			if fixture.reviews.tokens != tc.wantToken {
				t.Errorf("TokenReview calls = %d, want %d", fixture.reviews.tokens, tc.wantToken)
			}
			if tc.wantUser == "" {
				if len(fixture.reviews.sars) != 0 {
					t.Error("unauthenticated request reached authorization")
				}
				return
			}
			if len(fixture.reviews.sars) != 1 {
				t.Fatalf("SubjectAccessReview calls = %d, want 1", len(fixture.reviews.sars))
			}
			review := fixture.reviews.sars[0]
			if review.User != tc.wantUser || review.ResourceAttributes != nil || review.NonResourceAttributes == nil || review.NonResourceAttributes.Verb != "get" ||
				review.NonResourceAttributes.Path != "/metrics" {
				t.Errorf("unexpected authorization identity/attributes: %+v", review)
			}
			if tc.withCert && (!slices.Contains(review.Groups, "metrics-team") || !slices.Contains(review.Groups, "system:authenticated")) {
				t.Errorf("certificate groups were not mapped: %v", review.Groups)
			}
		})
	}
}

func TestMetricsClientCABundleRotation(t *testing.T) {
	first, second := newMetricsTestCA(t), newMetricsTestCA(t)
	fixture := newMetricsAuthFixture(t, first)
	leaf := first.client(t, "cert-reader", x509.ExtKeyUsageClientAuth, false)
	consumer := fixture.consumer(t, &leaf)
	if code, err := fixture.scrape(consumer, ""); err != nil || code != http.StatusOK {
		t.Fatalf("initial certificate request: %d, %v", code, err)
	}
	newLeaf := second.client(t, "cert-reader", x509.ExtKeyUsageClientAuth, false)
	// RunOnce exercises the same atomic reload used by the manager-owned watcher.
	for _, bundle := range [][]byte{append(bytes.Clone(first.pem), second.pem...), second.pem} {
		if err := os.WriteFile(fixture.caPath, bundle, 0600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.watcher.ca.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if code, err := fixture.scrape(fixture.consumer(t, &newLeaf), ""); err != nil || code != http.StatusOK {
			t.Fatalf("new issuer during CA rotation: %d, %v", code, err)
		}
		if bytes.HasPrefix(bundle, first.pem) {
			if code, err := fixture.scrape(consumer, ""); err != nil || code != http.StatusOK {
				t.Fatalf("old issuer during CA overlap: %d, %v", code, err)
			}
		}
	}
	if code, err := fixture.scrape(consumer, "valid-token"); err != nil || code != http.StatusUnauthorized {
		t.Fatalf("removed CA on reused connection: %d, %v; want 401 without token fallback", code, err)
	}
	consumer.Transport.(*http.Transport).CloseIdleConnections()
	if code, err := fixture.scrape(consumer, "valid-token"); err == nil {
		t.Fatalf("removed CA accepted on a new TLS connection: HTTP %d", code)
	}
	renewedLeaf := second.client(t, "cert-reader", x509.ExtKeyUsageClientAuth, false)
	if code, err := fixture.scrape(fixture.consumer(t, &renewedLeaf), ""); err != nil || code != http.StatusOK {
		t.Fatalf("renewed client certificate under the same CA: %d, %v", code, err)
	}
	for name, content := range map[string][]byte{"empty": {}, "malformed": []byte("not-a-certificate")} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(fixture.caPath, content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := fixture.watcher.ca.RunOnce(t.Context()); err == nil {
				t.Fatal("invalid CA update must fail")
			}
			if !bytes.Equal(fixture.watcher.ca.CurrentCABundleContent(), second.pem) {
				t.Fatal("invalid update replaced the last valid CA bundle")
			}
			if code, err := fixture.scrape(fixture.consumer(t, &newLeaf), ""); err != nil || code != http.StatusOK {
				t.Fatalf("last valid CA after invalid update: %d, %v", code, err)
			}
		})
	}
}

func TestMetricsClientCAStartupFailures(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MetricsAccessControl, true)
	for name, content := range map[string][]byte{"missing": nil, "empty": {}, "malformed": []byte("not-a-certificate")} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ca.crt")
			if content != nil {
				if err := os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := MetricsServerOptions(
				logr.Discard(),
				configapi.ControllerMetrics{BindAddress: ":8443", AccessControlMode: new(configapi.MetricsAccessControlModeClientCertificateOrBearer), ClientCAFile: new(path)},
			)
			if err == nil || !strings.Contains(err.Error(), "load metrics.clientCAFile:") {
				t.Fatalf("invalid startup CA did not fail loading: %v", err)
			}
		})
	}
	_, watcher, err := MetricsServerOptions(
		logr.Discard(),
		configapi.ControllerMetrics{BindAddress: "0", AccessControlMode: new(configapi.MetricsAccessControlModeClientCertificateOrBearer), ClientCAFile: new("/missing/ca.crt")},
	)
	if err != nil || watcher != nil {
		t.Fatalf("disabled metrics performed CA setup: watcher=%v, error=%v", watcher, err)
	}
}

type metricsCALoadListener chan struct{}

func (l metricsCALoadListener) Enqueue() {
	select {
	case l <- struct{}{}:
	default:
	}
}

func TestMetricsClientCAWatcher(t *testing.T) {
	cases := map[string]struct{ projected bool }{
		"atomic file replacement":              {},
		"projected volume symlink replacement": {projected: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			first, second := newMetricsTestCA(t), newMetricsTestCA(t)
			fixture := newMetricsAuthFixture(t, first)
			base := filepath.Dir(fixture.caPath)
			oldDirectory := filepath.Join(base, "..bundle-old")
			newDirectory := filepath.Join(base, "..bundle-new")
			path := fixture.caPath + ".new"
			if tc.projected {
				for _, directory := range []string{oldDirectory, newDirectory} {
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(fixture.caPath, filepath.Join(oldDirectory, "client-ca.crt")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("..bundle-old", filepath.Join(base, "..data")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join("..data", "client-ca.crt"), fixture.caPath); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(newDirectory, "client-ca.crt")
			}
			events := make(metricsCALoadListener, 1)
			fixture.watcher.ca.AddListener(events)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- fixture.watcher.Start(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("watcher shutdown: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("watcher did not stop on cancellation")
				}
			})
			if err := os.WriteFile(path, second.pem, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.projected {
				if err := os.Symlink("..bundle-new", filepath.Join(base, "..data-new")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(base, "..data-new"), filepath.Join(base, "..data")); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(oldDirectory); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Rename(path, fixture.caPath); err != nil {
				t.Fatal(err)
			}
			select {
			case <-events:
			case <-time.After(5 * time.Second):
				t.Fatal("watcher did not reload a replaced CA file")
			}
			if !bytes.Equal(fixture.watcher.ca.CurrentCABundleContent(), second.pem) {
				t.Fatal("watcher announced an update without publishing the new CA")
			}
			leaf := second.client(t, "cert-reader", x509.ExtKeyUsageClientAuth, false)
			if code, err := fixture.scrape(fixture.consumer(t, &leaf), ""); err != nil || code != http.StatusOK {
				t.Fatalf("watched CA not used for TLS and request authentication: %d, %v", code, err)
			}
		})
	}
}

func TestMetricsClientTLSKeepsServingOptions(t *testing.T) {
	fixture := newMetricsAuthFixture(t, newMetricsTestCA(t))
	want := &tls.Certificate{}
	config := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
	metricsClientTLSOption(fixture.watcher.ca)(config)
	// main and controller-runtime install serving-certificate callbacks after
	// applying the TLS options. The client-CA snapshot must preserve them.
	config.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return want, nil }
	snapshot, err := config.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := snapshot.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || got != want || snapshot.MinVersion != tls.VersionTLS13 || !slices.Equal(snapshot.NextProtos, config.NextProtos) {
		t.Fatalf("serving TLS options changed: certificate=%v, error=%v, config=%+v", got, err, snapshot)
	}
	if snapshot.ClientAuth != tls.VerifyClientCertIfGiven || snapshot.ClientCAs == nil {
		t.Fatal("client CA verification missing from handshake snapshot")
	}
}
