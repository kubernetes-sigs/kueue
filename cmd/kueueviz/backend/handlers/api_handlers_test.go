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

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"kueueviz/middleware"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newGetResourceTestRouter(objs ...runtime.Object) *gin.Engine {
	dynamicClient := dynamicfake.NewSimpleDynamicClient(k8sscheme.Scheme, objs...)

	router := gin.New()
	router.GET("/api/:resourceType/:name", GetResource(dynamicClient))
	return router
}

func TestGetResource(t *testing.T) {
	tests := map[string]struct {
		objs         []runtime.Object
		path         string
		wantStatus   int
		wantErrorSub string
	}{
		"unsupported output format": {
			path:         "/api/pod/my-pod?namespace=default",
			wantStatus:   http.StatusBadRequest,
			wantErrorSub: "Unsupported output format",
		},
		"unsupported resource type": {
			path:         "/api/bogus/my-thing?output=yaml",
			wantStatus:   http.StatusBadRequest,
			wantErrorSub: "Unsupported resource type: bogus",
		},
		"resource not found": {
			path:         "/api/pod/does-not-exist?namespace=default&output=yaml",
			wantStatus:   http.StatusNotFound,
			wantErrorSub: "Resource not found",
		},
		"success returns yaml content": {
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-pod",
						Namespace: "default",
					},
				},
			},
			path:       "/api/pod/my-pod?namespace=default&output=yaml",
			wantStatus: http.StatusOK,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			router := newGetResourceTestRouter(tc.objs...)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.wantStatus, w.Body.String())
			}

			if tc.wantErrorSub != "" {
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("failed to unmarshal error body: %v", err)
				}
				if !strings.Contains(body["error"], tc.wantErrorSub) {
					t.Fatalf("error = %q, want substring %q", body["error"], tc.wantErrorSub)
				}
				return
			}

			var resp ResourceResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal success body: %v", err)
			}
			if resp.Name != "my-pod" {
				t.Errorf("Name = %q, want %q", resp.Name, "my-pod")
			}
			if resp.Type != "pod" {
				t.Errorf("Type = %q, want %q", resp.Type, "pod")
			}
			if resp.Format != "yaml" {
				t.Errorf("Format = %q, want %q", resp.Format, "yaml")
			}
			if !strings.Contains(resp.Content, "my-pod") {
				t.Errorf("Content does not contain expected pod name:\n%s", resp.Content)
			}
		})
	}
}

type stubAuthorizer struct {
	allowed bool
	err     error
}

func (s stubAuthorizer) Authorize(_ context.Context, _ middleware.Identity, _ authorizationv1.ResourceAttributes) (bool, error) {
	return s.allowed, s.err
}

func callGetResource(authorizer middleware.Authorizer, dynClient dynamic.Interface, resourceType, name, namespace, username string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()

	router := gin.New()
	if username != "" {
		router.Use(func(c *gin.Context) {
			c.Set(middleware.ContextKeyIdentity, middleware.Identity{Username: username})
			c.Next()
		})
	}

	h := &Handlers{authorizer: authorizer}
	h.InitializeAPIRoutes(router, dynClient)

	req := httptest.NewRequest(
		http.MethodGet,
		fmt.Sprintf("/api/%s/%s?namespace=%s&output=yaml", resourceType, name, namespace),
		nil,
	)
	router.ServeHTTP(w, req)

	return w
}

func fixtureFor(resourceType, namespace, name, secret string) *unstructured.Unstructured {
	switch resourceType {
	case "pod":
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata":   map[string]any{"name": name, "namespace": namespace},
			"spec": map[string]any{
				"containers": []any{map[string]any{
					"name":  "app",
					"image": "registry.k8s.io/pause:3.9",
					"env":   []any{map[string]any{"name": "TENANT_SECRET", "value": secret}},
				}},
			},
		}}
	case "node":
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata": map[string]any{
				"name":        name,
				"annotations": map[string]any{"example.com/leaked": secret},
			},
		}}
	default: // workload
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kueue.x-k8s.io/v1beta2",
			"kind":       "Workload",
			"metadata": map[string]any{
				"name":        name,
				"namespace":   namespace,
				"annotations": map[string]any{"example.com/leaked": secret},
			},
		}}
	}
}

func TestGetResourceDeniesUnauthorizedCaller(t *testing.T) {
	const lowPrivUser = "system:serviceaccount:tenant-a:low-priv"

	tests := map[string]struct {
		resourceType string
		gvr          schema.GroupVersionResource
		listKind     string
		namespace    string
		name         string
		secret       string
	}{
		"namespaced Workload in another tenant's namespace": {
			resourceType: "workload", gvr: WorkloadsGVR(), listKind: "WorkloadList",
			namespace: "tenant-b", name: "victim-workload", secret: "secret-workload-annotation",
		},
		"namespaced Pod exposing a secret env var": {
			resourceType: "pod", gvr: PodsGVR(), listKind: "PodList",
			namespace: "tenant-b", name: "victim-pod", secret: "secret-pod-env-value",
		},
		"cluster-scoped Node": {
			resourceType: "node", gvr: NodesGVR(), listKind: "NodeList",
			namespace: "", name: "victim-node", secret: "secret-node-annotation",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				map[schema.GroupVersionResource]string{tc.gvr: tc.listKind},
				fixtureFor(tc.resourceType, tc.namespace, tc.name, tc.secret),
			)

			w := callGetResource(stubAuthorizer{allowed: false}, dynClient, tc.resourceType, tc.name, tc.namespace, lowPrivUser)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d: an unauthorized caller must be denied", w.Code, http.StatusForbidden)
			}
			if strings.Contains(w.Body.String(), tc.secret) {
				t.Errorf("response leaked resource content %q to caller %q, who is not authorized to read it",
					tc.secret, lowPrivUser)
			}
		})
	}
}

func TestGetResourceServesAuthorizedCaller(t *testing.T) {
	const secretValue = "served-secret-value"

	tests := map[string]struct {
		authorizer middleware.Authorizer
	}{
		"authorized caller is served the resource": {
			authorizer: stubAuthorizer{allowed: true},
		},
		"authorization disabled serves the resource": {
			authorizer: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				map[schema.GroupVersionResource]string{WorkloadsGVR(): "WorkloadList"},
				fixtureFor("workload", "tenant-b", "wl", secretValue),
			)

			w := callGetResource(tc.authorizer, dynClient, "workload", "wl", "tenant-b", "user")

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			var resp ResourceResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding response: %v; body=%s", err, w.Body.String())
			}
			if !strings.Contains(resp.Content, secretValue) {
				t.Fatalf("expected served resource to contain %q, got:\n%s", secretValue, resp.Content)
			}
		})
	}
}
