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

// Package centralizedtas holds the opt-in switch and shared constants for the
// centralized-TAS spike (non-production). The spike makes the MultiKueue
// manager globally authoritative for node-level topology placement across all
// worker clusters, treating each worker as the top (literal) topology level.
//
// It is intentionally gated on an environment variable rather than a feature
// gate so it stays completely inert in normal builds and deployments, and so
// the exploratory code is trivially removable.
package centralizedtas

import "os"

const (
	// envVar opts a manager process into the centralized-TAS spike.
	envVar = "KUEUE_CENTRALIZED_TAS_SPIKE"

	// ClusterLabel is stamped onto each remote Node copy fed into the manager
	// TAS cache so the manager can treat the worker cluster as the top
	// (literal) topology level.
	ClusterLabel = "kueue.x-k8s.io/multikueue-cluster"
)

// Enabled reports whether the centralized-TAS spike is on for this process.
func Enabled() bool {
	return os.Getenv(envVar) == "true"
}
