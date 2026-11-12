/*
Copyright The Kubeflow Authors.

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

package features

import (
	"k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
)

func init() {
	runtime.Must(utilfeature.DefaultMutableFeatureGate.Add(defaultFeatureGates))
	runtime.Must(utilfeature.DefaultMutableFeatureGate.AddDependencies(defaultFeatureGateDependencies))
}

const (
	// owner: robert-bell
	// kep: https://github.com/kubeflow/trainer/blob/main/proposals/2779-trainjob-progress/README.md
	//
	// Enables status server allowing TrainJob pods to update their status.
	TrainJobStatus featuregate.Feature = "TrainJobStatus"

	// owner: Sridhar1030
	// kep: https://github.com/kubeflow/trainer/blob/master/proposals/2782-dra-support/README.md
	//
	// Enables Dynamic Resource Allocation (DRA) support: trainer.resourceClaimsPerNode and
	// resourceClaims / resources.claims in runtimePatches.
	TrainJobDynamicResourceAllocation featuregate.Feature = "TrainJobDynamicResourceAllocation"

	// owner: andreyvelich
	// kep: https://github.com/kubeflow/trainer/blob/master/proposals/3015-workload-aware-scheduling/README.md
	//
	// Enables Workload-Aware Scheduling (WAS) support: spec.scheduling in TrainingRuntime and
	// ClusterTrainingRuntime, and the creation of Workload and PodGroup objects for TrainJobs.
	// Requires Kubernetes v1.37+ with the GenericWorkload feature gate enabled on kube-apiserver and
	// kube-scheduler, and the scheduling.k8s.io/v1beta1 API enabled on kube-apiserver.
	// Optional scheduling fields require additional Kubernetes feature gates:
	// TopologyAwareWorkloadScheduling for schedulingConstraints, and DRAWorkloadResourceClaims for
	// resourceClaims.
	TrainJobWorkloadAwareScheduling featuregate.Feature = "TrainJobWorkloadAwareScheduling"

	// owner: akshaychitneni
	// kep: https://github.com/kubeflow/trainer/blob/master/proposals/2605-optimization-job-crd/README.md
	//
	// Enables the OptimizationJob API for hyperparameter optimization. Requires TrainJobStatus,
	// because trials report their objective metrics through the TrainJob status server.
	OptimizationJob featuregate.Feature = "OptimizationJob"
)

// defaultFeatureGates consists of all known Trainer-specific feature keys.
// To add a new feature, define a key for it above and add it here. The features will be
// available throughout Trainer binaries.
//
// Entries are separated from each other with blank lines to avoid sweeping gofmt changes
// when adding or removing one entry.
var defaultFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	TrainJobStatus: {Default: false, PreRelease: featuregate.Alpha},

	TrainJobDynamicResourceAllocation: {Default: false, PreRelease: featuregate.Alpha},

	TrainJobWorkloadAwareScheduling: {Default: false, PreRelease: featuregate.Alpha},

	OptimizationJob: {Default: false, PreRelease: featuregate.Alpha},
}

// defaultFeatureGateDependencies lists the features that each feature depends on.
// Enabling a feature while any of its dependencies is disabled fails when the feature gates are set.
var defaultFeatureGateDependencies = map[featuregate.Feature][]featuregate.Feature{
	OptimizationJob: {TrainJobStatus},
}

// Enabled is helper for `utilfeature.DefaultFeatureGate.Enabled()`
func Enabled(f featuregate.Feature) bool {
	return utilfeature.DefaultFeatureGate.Enabled(f)
}
