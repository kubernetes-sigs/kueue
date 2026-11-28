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

// PerWorkerCluster identifies a request budget shared by all clients for one worker cluster.
const PerWorkerCluster = "worker-cluster"

// Scenario identifies the configuration a run used. Throughput and latency are only comparable
// between runs whose scenarios are identical, so the checker matches every field exactly instead
// of bounding it. Anything that measurably changes the result belongs here.
type Scenario struct {
	RemoteClientRateLimitScope string  `json:"remoteClientRateLimitScope"`
	WorkloadCount              int     `json:"workloadCount"`
	WorkerClusters             int     `json:"workerClusters"`
	CreationWorkers            int     `json:"creationWorkers"`
	CPURequest                 string  `json:"cpuRequest"`
	Dispatcher                 string  `json:"dispatcher"`
	WorkloadConcurrency        int     `json:"workloadConcurrency"`
	GCInterval                 string  `json:"gcInterval"`
	WorkerLostTimeout          string  `json:"workerLostTimeout"`
	EventsBatchPeriod          string  `json:"eventsBatchPeriod"`
	LocalClientQPS             float32 `json:"localClientQPS"`
	LocalClientBurst           int     `json:"localClientBurst"`
	RemoteClientQPS            float32 `json:"remoteClientQPS"`
	RemoteClientBurst          int     `json:"remoteClientBurst"`
	// Reconnect is set only for the worker reconnect scenario.
	Reconnect *ReconnectScenario `json:"reconnect,omitempty"`
}

type ReconnectScenario struct {
	WorkerMinRequestTimeout string `json:"workerMinRequestTimeout"`
	ProbeInterval           string `json:"probeInterval"`
	Reconnects              int    `json:"reconnects"`
	ProbesAfterReconnects   string `json:"probesAfterReconnects"`
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
	// Reconnect is set only for the worker reconnect scenario. The fields above then describe
	// the preloaded workloads, admitted before probing starts.
	Reconnect *ReconnectSummary `json:"reconnect,omitempty"`
}

// ReconnectSummary reports how worker reconnects delay new workloads. A reconnect window starts
// when the manager starts reconnecting to a worker and ends when the MultiKueue workload queue
// is empty again. Overlapping windows are merged.
type ReconnectSummary struct {
	// Windows lists the reconnect windows, relative to the start of probing.
	Windows []ReconnectWindow `json:"windows"`
	// ProbeAdmissionMs is the creation to manager admission latency of the probe workloads.
	ProbeAdmissionMs ProbeLatencies `json:"probeAdmissionMs"`
	// PeakQueueDepth is the highest sampled depth of the MultiKueue workload queue, by the
	// priority label of the workqueue_depth metric.
	PeakQueueDepth map[string]int `json:"peakQueueDepth"`
}

type ReconnectWindow struct {
	StartMs int64 `json:"startMs"`
	// DrainMs is the time from the start of the window until the queue is empty again.
	DrainMs int64 `json:"drainMs"`
	// PeakQueueDepth is the highest sampled depth of the MultiKueue workload queue in the
	// window, summed over priorities.
	PeakQueueDepth int `json:"peakQueueDepth"`
	// WorkloadReconciles counts the MultiKueue workload reconciles in the window.
	WorkloadReconciles int `json:"workloadReconciles"`
}

// ProbeLatencies splits the probes by when they were pending. A probe pending at any time in a
// reconnect window is DuringReconnect. The other probes are BeforeReconnect when created before
// the first window and AfterReconnect otherwise.
type ProbeLatencies struct {
	BeforeReconnect Durations `json:"beforeReconnect"`
	DuringReconnect Durations `json:"duringReconnect"`
	AfterReconnect  Durations `json:"afterReconnect"`
}
