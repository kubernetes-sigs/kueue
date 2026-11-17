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

// Package config defines the executable MultiKueue benchmark scenario.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/test/performance/multikueue/report"
)

// maxWorkloadCount bounds the runner's O(N) observation state while allowing 10k-workload
// scale scenarios.
const maxWorkloadCount = 10_000

// Config contains all configurable inputs that affect the measured scenario.
// Values are explicit so changing production defaults cannot silently recalibrate the benchmark.
type Config struct {
	WorkloadCount       int             `json:"workloadCount"`
	WorkerClusters      int             `json:"workerClusters"`
	CreationWorkers     int             `json:"creationWorkers"`
	CPURequest          string          `json:"cpuRequest"`
	LocalClientQPS      float32         `json:"localClientQPS"`
	LocalClientBurst    int32           `json:"localClientBurst"`
	RemoteClientQPS     float32         `json:"remoteClientQPS"`
	RemoteClientBurst   int32           `json:"remoteClientBurst"`
	WorkloadConcurrency int             `json:"workloadConcurrency"`
	GCInterval          metav1.Duration `json:"gcInterval"`
	WorkerLostTimeout   metav1.Duration `json:"workerLostTimeout"`
	EventsBatchPeriod   metav1.Duration `json:"eventsBatchPeriod"`
	Dispatcher          string          `json:"dispatcher"`
	Timeout             metav1.Duration `json:"timeout"`
	// Reconnect, when set, adds the worker reconnect phase after the workloads are admitted.
	Reconnect *Reconnect `json:"reconnect,omitempty"`
}

// Reconnect configures the worker reconnect scenario. The worker API servers end every watch
// that does not request a timeout after WorkerMinRequestTimeout to twice that value, which makes
// MultiKueue reconnect to the worker and requeue its workloads, as on a production manager.
type Reconnect struct {
	// WorkerMinRequestTimeout sets --min-request-timeout on the worker API servers. The API
	// server only accepts whole seconds.
	WorkerMinRequestTimeout metav1.Duration `json:"workerMinRequestTimeout"`
	// ProbeInterval is the fixed interval between the probe workloads created after the
	// preloaded workloads are admitted.
	ProbeInterval metav1.Duration `json:"probeInterval"`
	// Reconnects is the number of observed reconnects after which probing stops.
	Reconnects int `json:"reconnects"`
	// ProbesAfterReconnects keeps probing for this long after the last observed reconnect has
	// drained, so the summary includes probes after a reconnect.
	ProbesAfterReconnects metav1.Duration `json:"probesAfterReconnects"`
}

// Load reads and validates a benchmark configuration.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks that every executable setting is supported and explicitly configured.
func (c Config) Validate() error {
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
	if c.LocalClientQPS <= 0 {
		return errors.New("localClientQPS must be positive")
	}
	if c.LocalClientBurst <= 0 {
		return errors.New("localClientBurst must be positive")
	}
	if c.WorkloadConcurrency <= 0 {
		return errors.New("workloadConcurrency must be positive")
	}
	if c.GCInterval.Duration <= 0 {
		return errors.New("gcInterval must be positive")
	}
	if c.WorkerLostTimeout.Duration <= 0 {
		return errors.New("workerLostTimeout must be positive")
	}
	if c.EventsBatchPeriod.Duration <= 0 {
		return errors.New("eventsBatchPeriod must be positive")
	}
	if c.Dispatcher != configapi.MultiKueueDispatcherModeAllAtOnce {
		return fmt.Errorf("dispatcher must be %s", configapi.MultiKueueDispatcherModeAllAtOnce)
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
	if c.Reconnect != nil {
		if err := c.Reconnect.validate(c.WorkloadCount, c.Timeout.Duration); err != nil {
			return fmt.Errorf("reconnect: %w", err)
		}
	}
	return nil
}

func (r *Reconnect) validate(workloadCount int, timeout time.Duration) error {
	if r.WorkerMinRequestTimeout.Duration < time.Second {
		return errors.New("workerMinRequestTimeout must be at least 1s")
	}
	if r.WorkerMinRequestTimeout.Duration%time.Second != 0 {
		return errors.New("workerMinRequestTimeout must be a whole number of seconds")
	}
	if r.ProbeInterval.Duration <= 0 {
		return errors.New("probeInterval must be positive")
	}
	if r.Reconnects < 1 {
		return errors.New("reconnects must be positive")
	}
	if r.ProbesAfterReconnects.Duration <= 0 {
		return errors.New("probesAfterReconnects must be positive")
	}
	if workloadCount+r.MaxProbes(timeout) > maxWorkloadCount {
		return fmt.Errorf("workloadCount plus the probes that fit in the timeout must not exceed %d", maxWorkloadCount)
	}
	return nil
}

// MaxProbes bounds the number of probe workloads a run can create before it times out. The
// runner reserves quota for them.
func (r *Reconnect) MaxProbes(timeout time.Duration) int {
	return int(timeout / r.ProbeInterval.Duration)
}

// Scenario identifies the configuration used by the runner and required by the checker.
// Timeout is a safety bound and does not affect a successful run's measurements.
func (c Config) Scenario() report.Scenario {
	return report.Scenario{
		RemoteClientRateLimitScope: report.PerWorkerCluster,
		WorkloadCount:              c.WorkloadCount,
		WorkerClusters:             c.WorkerClusters,
		CreationWorkers:            c.CreationWorkers,
		CPURequest:                 c.CPURequest,
		Dispatcher:                 c.Dispatcher,
		WorkloadConcurrency:        c.WorkloadConcurrency,
		GCInterval:                 c.GCInterval.Duration.String(),
		WorkerLostTimeout:          c.WorkerLostTimeout.Duration.String(),
		EventsBatchPeriod:          c.EventsBatchPeriod.Duration.String(),
		LocalClientQPS:             c.LocalClientQPS,
		LocalClientBurst:           int(c.LocalClientBurst),
		RemoteClientQPS:            c.RemoteClientQPS,
		RemoteClientBurst:          int(c.RemoteClientBurst),
		Reconnect:                  c.Reconnect.scenario(),
	}
}

func (r *Reconnect) scenario() *report.ReconnectScenario {
	if r == nil {
		return nil
	}
	return &report.ReconnectScenario{
		WorkerMinRequestTimeout: r.WorkerMinRequestTimeout.Duration.String(),
		ProbeInterval:           r.ProbeInterval.Duration.String(),
		Reconnects:              r.Reconnects,
		ProbesAfterReconnects:   r.ProbesAfterReconnects.Duration.String(),
	}
}
