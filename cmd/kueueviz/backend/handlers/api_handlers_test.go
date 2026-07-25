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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
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
