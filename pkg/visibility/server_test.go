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

package visibility

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/util/tlsconfig"
)

func TestVisibilityServerStarts_WithCustomKubeconfig(t *testing.T) {
	// Wipe out in-cluster environment variables to prevent silent fallbacks.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	// Spin up a mock Kubernetes API server to intercept the authentication ConfigMap request
	mockKubeAPI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The visibility server will request this configmap during startup to configure delegated auth
		if r.URL.Path == "/api/v1/namespaces/kube-system/configmaps/extension-apiserver-authentication" {
			w.Header().Set("Content-Type", "application/json")
			// Return a minimal valid ConfigMap
			w.Write([]byte(`{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "extension-apiserver-authentication",
					"namespace": "kube-system"
				},
				"data": {}
			}`))
			return
		}
		// Return 200 OK for any other discovery requests
		w.WriteHeader(http.StatusOK)
	}))
	defer mockKubeAPI.Close()

	// Setup a temporary directory for our dummy kubeconfig and certs
	tempDir := t.TempDir()
	kubeconfigPath := filepath.Join(tempDir, "dummy.kubeconfig")

	// Create a kubeconfig that points directly to our mock server's random port
	dummyConfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"dummy-cluster": {
				Server:                mockKubeAPI.URL, // Use the mock server's URL!
				InsecureSkipTLSVerify: true,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"dummy-user": {Token: "dummy-token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"dummy-context": {
				Cluster:  "dummy-cluster",
				AuthInfo: "dummy-user",
			},
		},
		CurrentContext: "dummy-context",
	}

	if err := clientcmd.WriteToFile(dummyConfig, kubeconfigPath); err != nil {
		t.Fatalf("Failed to write dummy kubeconfig: %v", err)
	}

	// Safely set the kubeconfig flag
	originalFlag := flag.Lookup("kubeconfig")
	var originalValue string
	if originalFlag == nil {
		flag.String("kubeconfig", "", "path to kubeconfig")
	} else {
		originalValue = originalFlag.Value.String()
		defer func() { _ = flag.Set("kubeconfig", originalValue) }()
	}
	_ = flag.Set("kubeconfig", kubeconfigPath)

	// Override certDir so self-signed certs write to our temp dir instead of /visibility
	originalCertDir := certDir
	certDir = tempDir
	defer func() { certDir = originalCertDir }()

	// Prepare dependencies
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dummyRestConfig := &rest.Config{
		Host:            mockKubeAPI.URL,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}

	// Pass nil for the queue manager
	var dummyKueueMgr *queue.Manager = nil

	// Start the server in a goroutine
	serverErrCh := make(chan error, 1)
	go func() {
		err := CreateAndStartVisibilityServer(ctx, dummyKueueMgr, true, dummyRestConfig, &tlsconfig.TLS{})
		if err != nil && err != context.Canceled {
			serverErrCh <- err
		}
		close(serverErrCh)
	}()

	// Poll the server's endpoint to verify it actually started
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 2 * time.Second,
	}

	// 8082 is the default BindPort in createVisibilityServerOptions
	serverURL := "https://127.0.0.1:8082/openapi/v2"

	requireServerReady(t, client, serverURL, serverErrCh)

	// Server is up! Canceling context to trigger clean shutdown
	cancel()

	// Wait for shutdown to complete
	select {
	case err := <-serverErrCh:
		if err != nil {
			t.Errorf("Server shut down with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("Timed out waiting for server to shut down")
	}
}

// requireServerReady polls the server until it accepts a connection and returns any HTTP response
func requireServerReady(t *testing.T, client *http.Client, url string, errCh <-chan error) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-errCh:
			t.Fatalf("Server failed to start: %v", err)
		case <-timeout:
			t.Fatalf("Timed out waiting for server to become ready at %s", url)
		case <-ticker.C:
			resp, err := client.Get(url)
			// If err == nil, it means the TCP connection succeeded and we got a valid HTTP response.
			// We don't care if it's a 200, 401, 403, or 500. The fact that it responded
			// proves the server successfully started and is serving traffic.
			if err == nil {
				resp.Body.Close()
				return
			}
		}
	}
}
