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

package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"
)

// TestMain temporarily records the CI workload to guide test-only optimizations.
// This instrumentation is removed after collecting the diagnostic artifact.
func TestMain(m *testing.M) {
	artifacts := os.Getenv("ARTIFACTS")
	if artifacts == "" {
		os.Exit(m.Run())
	}
	profile, err := os.Create(filepath.Join(artifacts, "scheduler-cpu.pprof"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := pprof.StartCPUProfile(profile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	pprof.StopCPUProfile()
	if err := profile.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
