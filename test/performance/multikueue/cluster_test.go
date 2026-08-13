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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestBenchmarkEnvironmentDoesNotUseExistingCluster(t *testing.T) {
	testEnv := benchmarkEnvironment("crds", runtime.NewScheme())
	if testEnv.UseExistingCluster == nil || *testEnv.UseExistingCluster {
		t.Fatal("UseExistingCluster must be explicitly disabled")
	}
}

func TestWaitForManagerReady(t *testing.T) {
	startErr := errors.New("bind failed")

	testCases := map[string]struct {
		synced  bool
		doneErr error
		sendErr bool
		wantErr string
	}{
		"cache synced": {
			synced: true,
		},
		"cache sync failed": {
			wantErr: "wait for worker-1 manager cache sync",
		},
		"start error is preferred over cache sync failure": {
			doneErr: startErr,
			sendErr: true,
			wantErr: "start worker-1 manager: bind failed",
		},
		"manager exited cleanly before cache sync": {
			sendErr: true,
			wantErr: "worker-1 manager exited before cache sync",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			if tc.sendErr {
				done <- tc.doneErr
			}

			err := waitForManagerReady("worker-1", func(context.Context) bool {
				return tc.synced
			}, done, context.Background())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("waitForManagerReady() unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("waitForManagerReady() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestWaitForManagerReadyReportsStartErrorWithoutWaitingForCacheSync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	done <- errors.New("bind failed")

	err := waitForManagerReady("worker-1", func(ctx context.Context) bool {
		<-ctx.Done()
		return false
	}, done, ctx)
	if err == nil || !strings.Contains(err.Error(), "start worker-1 manager: bind failed") {
		t.Fatalf("waitForManagerReady() error = %v, want start error", err)
	}
}

func TestWaitForManagerReadyDoesNotConsumeDoneOnSuccess(t *testing.T) {
	done := make(chan error, 1)
	if err := waitForManagerReady("worker-1", func(context.Context) bool {
		return true
	}, done, context.Background()); err != nil {
		t.Fatalf("waitForManagerReady() unexpected error: %v", err)
	}

	select {
	case done <- errors.New("still running"):
	default:
		t.Fatal("successful cache sync consumed the manager done channel")
	}
}

func TestStopErrors(t *testing.T) {
	managerErr := errors.New("manager stop failed")
	envErr := errors.New("control plane stop failed")

	testCases := map[string]struct {
		managerErr error
		envErr     error
		want       []string
	}{
		"neither": {},
		"manager only": {
			managerErr: managerErr,
			want:       []string{"stop worker-1 manager: manager stop failed"},
		},
		"control plane only": {
			envErr: envErr,
			want:   []string{"stop worker-1 control plane: control plane stop failed"},
		},
		"both": {
			managerErr: managerErr,
			envErr:     envErr,
			want: []string{
				"stop worker-1 manager: manager stop failed",
				"stop worker-1 control plane: control plane stop failed",
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := stopErrors("worker-1", tc.managerErr, tc.envErr)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("stopErrors() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("stopErrors() returned no error")
			}
			got := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("stopErrors() error %q does not contain %q", got, want)
				}
			}
		})
	}
}
