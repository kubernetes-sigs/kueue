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
		config  benchmarkConfig
		valid   bool
		wantErr string
	}{
		"valid": {
			config: valid,
			valid:  true,
		},
		"maximum workloads": {
			config: withWorkloadCount(valid, maxWorkloadCount),
			valid:  true,
		},
		"too many workloads": {
			config:  withWorkloadCount(valid, maxWorkloadCount+1),
			wantErr: "workloadCount must not exceed 10000",
		},
		"zero workloads": {
			config: withWorkloadCount(valid, 0),
		},
		"zero workers": {
			config: withWorkerClusters(valid, 0),
		},
		"zero creation workers": {
			config: withCreationWorkers(valid, 0),
		},
		"missing remote QPS": {
			config:  withRemoteClientQPS(valid, 0),
			wantErr: "remoteClientQPS must be positive",
		},
		"negative remote QPS": {
			config:  withRemoteClientQPS(valid, -1),
			wantErr: "remoteClientQPS must be positive",
		},
		"missing remote burst": {
			config:  withRemoteClientBurst(valid, 0),
			wantErr: "remoteClientBurst must be positive",
		},
		"negative remote burst": {
			config:  withRemoteClientBurst(valid, -1),
			wantErr: "remoteClientBurst must be positive",
		},
		"invalid CPU": {
			config: withCPURequest(valid, "not-a-quantity"),
		},
		"zero CPU": {
			config: withCPURequest(valid, "0"),
		},
		"zero timeout": {
			config: withTimeout(valid, 0),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := tc.config.validate()
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

func withWorkloadCount(cfg benchmarkConfig, value int) benchmarkConfig {
	cfg.WorkloadCount = value
	return cfg
}

func withWorkerClusters(cfg benchmarkConfig, value int) benchmarkConfig {
	cfg.WorkerClusters = value
	return cfg
}

func withCreationWorkers(cfg benchmarkConfig, value int) benchmarkConfig {
	cfg.CreationWorkers = value
	return cfg
}

func withRemoteClientQPS(cfg benchmarkConfig, value float32) benchmarkConfig {
	cfg.RemoteClientQPS = value
	return cfg
}

func withRemoteClientBurst(cfg benchmarkConfig, value int32) benchmarkConfig {
	cfg.RemoteClientBurst = value
	return cfg
}

func withCPURequest(cfg benchmarkConfig, value string) benchmarkConfig {
	cfg.CPURequest = value
	return cfg
}

func withTimeout(cfg benchmarkConfig, value time.Duration) benchmarkConfig {
	cfg.Timeout.Duration = value
	return cfg
}
