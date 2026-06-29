/*
Copyright The Kubeflow Authors.

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

package statusserver

import (
	"fmt"
	"net/http"

	"github.com/go-logr/logr"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Middleware func(http.Handler) http.Handler

// chain applies middleware in order: first middleware wraps second, etc.
func chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// recoveryMiddleware recovers from panics in HTTP handlers to prevent Server crashes.
func recoveryMiddleware(log logr.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Error(fmt.Errorf("panic: %v", err), "Panic in HTTP handler",
						"path", r.URL.Path, "method", r.Method)
					badRequest(w, log, "Internal Server Error", v1.StatusReasonInternalError, http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// loggingMiddleware logs incoming HTTP requests.
func loggingMiddleware(log logr.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.V(5).Info("HTTP request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
			next.ServeHTTP(w, r)
		})
	}
}

// bodySizeLimitMiddleware enforces a maximum request body size.
func bodySizeLimitMiddleware(log logr.Logger, maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reject based on Content-Length header if present
			if r.ContentLength > maxBytes {
				badRequest(w, log, "Payload too large",
					v1.StatusReasonRequestEntityTooLarge,
					http.StatusRequestEntityTooLarge)
				return
			}

			// Wrap body to enforce limit for chunked/streaming requests
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
