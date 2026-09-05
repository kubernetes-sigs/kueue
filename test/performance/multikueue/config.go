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
	"errors"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// maxWorkloadCount bounds the runner's O(N) observation state while allowing 10k-workload
// scale scenarios.
const maxWorkloadCount = 10_000

type benchmarkConfig struct {
	WorkloadCount     int             `json:"workloadCount"`
	WorkerClusters    int             `json:"workerClusters"`
	CreationWorkers   int             `json:"creationWorkers"`
	RemoteClientQPS   float32         `json:"remoteClientQPS"`
	RemoteClientBurst int32           `json:"remoteClientBurst"`
	CPURequest        string          `json:"cpuRequest"`
	Timeout           metav1.Duration `json:"timeout"`
}

func loadConfig(path string) (benchmarkConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return benchmarkConfig{}, fmt.Errorf("read config: %w", err)
	}

	var cfg benchmarkConfig
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return benchmarkConfig{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return benchmarkConfig{}, err
	}
	return cfg, nil
}

func (c benchmarkConfig) validate() error {
	if c.WorkloadCount < 1 {
		return errors.New("workloadCount must be positive")
	}
	if c.WorkloadCount > maxWorkloadCount {
		return fmt.Errorf("workloadCount must not exceed %d", maxWorkloadCount)
	}
	if c.WorkerClusters < 1 {
		return errors.New("workerClusters must be positive")
	}
	if c.CreationWorkers < 1 {
		return errors.New("creationWorkers must be positive")
	}
	if c.RemoteClientQPS <= 0 {
		return errors.New("remoteClientQPS must be positive")
	}
	if c.RemoteClientBurst <= 0 {
		return errors.New("remoteClientBurst must be positive")
	}
	cpuRequest, err := resource.ParseQuantity(c.CPURequest)
	if err != nil {
		return fmt.Errorf("cpuRequest must be a valid resource quantity: %w", err)
	}
	if cpuRequest.Sign() <= 0 {
		return errors.New("cpuRequest must be positive")
	}
	if c.Timeout.Duration <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}
