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
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type managerRunnerFunc func(context.Context) error

func (f managerRunnerFunc) Start(ctx context.Context) error {
	return f(ctx)
}

type fakeHTTPServer struct {
	listenAndServe func() error
	shutdown       func(context.Context) error
}

func (s *fakeHTTPServer) ListenAndServe() error {
	return s.listenAndServe()
}

func (s *fakeHTTPServer) Shutdown(ctx context.Context) error {
	return s.shutdown(ctx)
}

func TestRunComponents(t *testing.T) {
	managerErr := errors.New("manager failed")
	managerShutdownErr := errors.New("manager shutdown failed")
	serverErr := errors.New("server failed")
	shutdownErr := errors.New("shutdown failed")

	tests := map[string]struct {
		cancelBeforeRun          bool
		managerErr               error
		managerShutdownErr       error
		managerStopsUnexpectedly bool
		managerDoesNotStop       bool
		serverErr                error
		serverStopsUnexpectedly  bool
		shutdownErr              error
		shutdownTimeout          time.Duration
		wantErrors               []error
		wantErrorSubstrings      []string
	}{
		"context canceled": {
			cancelBeforeRun: true,
		},
		"manager fails": {
			managerErr: managerErr,
			wantErrors: []error{managerErr},
		},
		"manager cleanup fails": {
			cancelBeforeRun:    true,
			managerShutdownErr: managerShutdownErr,
			wantErrors:         []error{managerShutdownErr},
		},
		"manager stops unexpectedly": {
			managerStopsUnexpectedly: true,
			wantErrorSubstrings:      []string{"manager stopped unexpectedly"},
		},
		"HTTP server fails": {
			serverErr:  serverErr,
			wantErrors: []error{serverErr},
		},
		"HTTP server failure and manager cleanup failure": {
			managerShutdownErr: managerShutdownErr,
			serverErr:          serverErr,
			wantErrors:         []error{managerShutdownErr, serverErr},
		},
		"manager and HTTP server fail": {
			managerErr: managerErr,
			serverErr:  serverErr,
			wantErrors: []error{managerErr, serverErr},
		},
		"HTTP server stops unexpectedly": {
			serverStopsUnexpectedly: true,
			wantErrorSubstrings:     []string{"HTTP server stopped unexpectedly"},
		},
		"shutdown fails after manager failure": {
			managerErr:  managerErr,
			shutdownErr: shutdownErr,
			wantErrors:  []error{managerErr, shutdownErr},
		},
		"component shutdown times out": {
			cancelBeforeRun:    true,
			managerDoesNotStop: true,
			wantErrors:         []error{context.DeadlineExceeded},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelBeforeRun {
				cancel()
			}

			serverStopped := make(chan struct{})
			managerRelease := make(chan struct{})
			manager := managerRunnerFunc(func(ctx context.Context) error {
				if tc.managerErr != nil {
					return tc.managerErr
				}
				if tc.managerStopsUnexpectedly {
					return nil
				}
				if tc.managerDoesNotStop {
					<-managerRelease
					return nil
				}
				<-ctx.Done()
				return tc.managerShutdownErr
			})
			server := &fakeHTTPServer{
				listenAndServe: func() error {
					if tc.serverErr != nil {
						return tc.serverErr
					}
					if tc.serverStopsUnexpectedly {
						return http.ErrServerClosed
					}
					<-serverStopped
					return http.ErrServerClosed
				},
				shutdown: func(context.Context) error {
					close(serverStopped)
					return tc.shutdownErr
				},
			}

			shutdownTimeout := tc.shutdownTimeout
			if !tc.managerDoesNotStop {
				shutdownTimeout = time.Second
			}
			gotErr := runComponents(ctx, manager, server, shutdownTimeout)
			close(managerRelease)

			if len(tc.wantErrors) == 0 && len(tc.wantErrorSubstrings) == 0 && gotErr != nil {
				t.Fatalf("runComponents() error = %v, want nil", gotErr)
			}
			for _, wantErr := range tc.wantErrors {
				if !errors.Is(gotErr, wantErr) {
					t.Errorf("runComponents() error = %v, want an error wrapping %v", gotErr, wantErr)
				}
			}
			for _, wantSubstring := range tc.wantErrorSubstrings {
				if gotErr == nil || !strings.Contains(gotErr.Error(), wantSubstring) {
					t.Errorf("runComponents() error = %v, want an error containing %q", gotErr, wantSubstring)
				}
			}
		})
	}
}

func TestWaitForShutdownPrefersCompletedComponents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	componentDone := make(chan struct{}, 2)
	componentDone <- struct{}{}
	componentDone <- struct{}{}

	if err := waitForShutdown(ctx, componentDone, 2); err != nil {
		t.Errorf("waitForShutdown() error = %v, want nil", err)
	}
}
