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
	"time"

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
			managerRun := &managerRun{
				done: make(chan struct{}),
				err:  tc.doneErr,
			}
			if tc.sendErr {
				close(managerRun.done)
			}

			err := waitForManagerReady(t.Context(), "worker-1", func(context.Context) bool {
				return tc.synced
			}, managerRun)
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
	managerRun := &managerRun{
		done: make(chan struct{}),
		err:  errors.New("bind failed"),
	}
	close(managerRun.done)

	err := waitForManagerReady(t.Context(), "worker-1", func(ctx context.Context) bool {
		<-ctx.Done()
		return false
	}, managerRun)
	if err == nil || !strings.Contains(err.Error(), "start worker-1 manager: bind failed") {
		t.Fatalf("waitForManagerReady() error = %v, want start error", err)
	}
}

func TestWaitForManagerReadyDoesNotConsumeDoneOnSuccess(t *testing.T) {
	managerErr := errors.New("still running")
	managerRun := &managerRun{done: make(chan struct{})}
	if err := waitForManagerReady(t.Context(), "worker-1", func(context.Context) bool {
		return true
	}, managerRun); err != nil {
		t.Fatalf("waitForManagerReady() unexpected error: %v", err)
	}

	managerRun.err = managerErr
	close(managerRun.done)
	<-managerRun.done
	if !errors.Is(managerRun.err, managerErr) {
		t.Fatalf("manager error = %v, want %v", managerRun.err, managerErr)
	}
}

func TestStartManagerRunReportsUnexpectedExit(t *testing.T) {
	managerErr := errors.New("manager failed")
	clusterCtx, failClusters := context.WithCancelCause(t.Context())
	defer failClusters(nil)

	managerRun := startManagerRun(clusterCtx, "worker-1", func(context.Context) error {
		return managerErr
	}, failClusters)

	select {
	case <-clusterCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("manager exit did not cancel the cluster context")
	}
	if cause := context.Cause(clusterCtx); !errors.Is(cause, managerErr) || !strings.Contains(cause.Error(), "worker-1 manager exited unexpectedly") {
		t.Fatalf("cluster context cause = %v, want worker-1 manager failure", cause)
	}
	<-managerRun.done
	if !errors.Is(managerRun.err, managerErr) {
		t.Fatalf("latched manager error = %v, want %v", managerRun.err, managerErr)
	}
}

func TestStartManagerRunIgnoresExpectedCancellation(t *testing.T) {
	managerCtx, cancelManager := context.WithCancel(t.Context())
	reported := make(chan error, 1)
	managerRun := startManagerRun(managerCtx, "worker-1", func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}, func(err error) {
		reported <- err
	})

	cancelManager()
	select {
	case <-managerRun.done:
	case <-time.After(time.Second):
		t.Fatal("manager did not stop after cancellation")
	}
	if managerRun.unexpected {
		t.Fatal("expected manager cancellation was reported as an unexpected exit")
	}
	select {
	case err := <-reported:
		t.Fatalf("unexpected manager exit reported: %v", err)
	default:
	}
}

func TestUnexpectedManagerErrorObservesLatchedExit(t *testing.T) {
	managerErr := errors.New("manager failed")
	startupErr := errors.New("start next cluster")
	managerRun := &managerRun{
		done:       make(chan struct{}),
		err:        managerErr,
		unexpected: true,
	}
	close(managerRun.done)

	err := unexpectedManagerError(t.Context(), t.Context(), &benchmarkCluster{
		name:    "worker-1",
		manager: managerRun,
	})
	if !errors.Is(err, managerErr) || !strings.Contains(err.Error(), "worker-1 manager exited unexpectedly") {
		t.Fatalf("unexpectedManagerError() = %v, want worker-1 manager failure", err)
	}

	err = preferUnexpectedManagerError(startupErr, &benchmarkCluster{
		name:    "worker-1",
		manager: managerRun,
	})
	if !errors.Is(err, managerErr) || errors.Is(err, startupErr) {
		t.Fatalf("preferUnexpectedManagerError() = %v, want established manager failure", err)
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
