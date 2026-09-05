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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBenchmarkConfigValidate(t *testing.T) {
	valid := benchmarkConfig{
		WorkloadCount:     1000,
		WorkerClusters:    3,
		CreationWorkers:   20,
		RemoteClientQPS:   1000,
		RemoteClientBurst: 1000,
		CPURequest:        "1m",
		Timeout:           metav1.Duration{Duration: 30 * time.Minute},
	}

	testCases := map[string]struct {
		mutate  func(*benchmarkConfig)
		valid   bool
		wantErr string
	}{
		"valid": {
			valid: true,
		},
		"maximum workloads": {
			mutate: func(c *benchmarkConfig) { c.WorkloadCount = maxWorkloadCount },
			valid:  true,
		},
		"too many workloads": {
			mutate:  func(c *benchmarkConfig) { c.WorkloadCount = maxWorkloadCount + 1 },
			wantErr: "workloadCount must not exceed 10000",
		},
		"zero workloads": {
			mutate: func(c *benchmarkConfig) { c.WorkloadCount = 0 },
		},
		"zero workers": {
			mutate: func(c *benchmarkConfig) { c.WorkerClusters = 0 },
		},
		"zero creation workers": {
			mutate: func(c *benchmarkConfig) { c.CreationWorkers = 0 },
		},
		"missing remote QPS": {
			mutate:  func(c *benchmarkConfig) { c.RemoteClientQPS = 0 },
			wantErr: "remoteClientQPS must be positive",
		},
		"negative remote QPS": {
			mutate:  func(c *benchmarkConfig) { c.RemoteClientQPS = -1 },
			wantErr: "remoteClientQPS must be positive",
		},
		"missing remote burst": {
			mutate:  func(c *benchmarkConfig) { c.RemoteClientBurst = 0 },
			wantErr: "remoteClientBurst must be positive",
		},
		"negative remote burst": {
			mutate:  func(c *benchmarkConfig) { c.RemoteClientBurst = -1 },
			wantErr: "remoteClientBurst must be positive",
		},
		"invalid CPU": {
			mutate: func(c *benchmarkConfig) { c.CPURequest = "not-a-quantity" },
		},
		"zero CPU": {
			mutate: func(c *benchmarkConfig) { c.CPURequest = "0" },
		},
		"zero timeout": {
			mutate: func(c *benchmarkConfig) { c.Timeout.Duration = 0 },
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			err := cfg.validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validate() returned no error")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}
