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

// Package report holds the MultiKueue benchmark's result schema. It is separate from the runner so
// that the regression checker consumes the same declaration the runner writes, rather than a copy
// that can drift from it.
package report

// Scenario identifies the configuration a run used. Throughput and latency are only comparable
// between runs whose scenarios are identical, so the checker matches every field exactly instead
// of bounding it. Anything that measurably changes the result belongs here.
type Scenario struct {
	WorkloadCount       int     `json:"workloadCount"`
	WorkerClusters      int     `json:"workerClusters"`
	CreationWorkers     int     `json:"creationWorkers"`
	CPURequest          string  `json:"cpuRequest"`
	Dispatcher          string  `json:"dispatcher"`
	WorkloadConcurrency int     `json:"workloadConcurrency"`
	GCInterval          string  `json:"gcInterval"`
	WorkerLostTimeout   string  `json:"workerLostTimeout"`
	EventsBatchPeriod   string  `json:"eventsBatchPeriod"`
	LocalClientQPS      float32 `json:"localClientQPS"`
	LocalClientBurst    int     `json:"localClientBurst"`
	RemoteClientQPS     float32 `json:"remoteClientQPS"`
	RemoteClientBurst   int     `json:"remoteClientBurst"`
}

// Build records runtime metadata. GoVersion and Platform are always populated; GitVersion and
// GitCommit identify the measured source only when build link flags override the package fallbacks,
// as the Make target does.
type Build struct {
	GitVersion string `json:"gitVersion"`
	GitCommit  string `json:"gitCommit"`
	GoVersion  string `json:"goVersion"`
	Platform   string `json:"platform"`
}

type Timing struct {
	GenerationMs int64 `json:"generationMs"`
	TotalMs      int64 `json:"totalMs"`
	DrainMs      int64 `json:"drainMs"`
}

// Latencies reports manager-side quota reservation and end-to-end admission. AdmissionMs includes
// MultiKueue dispatch and the subsequent core-controller reconcile that admits the local Workload,
// so the difference between the two series must not be attributed solely to MultiKueue.
type Latencies struct {
	QuotaReservationMs Durations `json:"quotaReservationMs"`
	AdmissionMs        Durations `json:"admissionMs"`
}

type Summary struct {
	Build               Build     `json:"build"`
	Scenario            Scenario  `json:"scenario"`
	Timing              Timing    `json:"timing"`
	ThroughputPerSecond float64   `json:"throughputPerSecond"`
	Latencies           Latencies `json:"latencies"`
	// WatchGaps is the number of times the run re-established its Workload watch. A resumed watch
	// replays what it missed, and those transitions are timestamped on arrival. A gap can inflate
	// latencies and total/drain times and can reduce measured throughput when it delays the final
	// admission observation.
	WatchGaps          int            `json:"watchGaps"`
	WorkerDistribution map[string]int `json:"workerDistribution"`
}
