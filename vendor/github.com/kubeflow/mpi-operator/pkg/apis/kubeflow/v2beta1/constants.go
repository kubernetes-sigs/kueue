// Copyright 2019 The Kubeflow Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v2beta1

const (
	// EnvKubeflowNamespace is ENV for kubeflow namespace specified by user.
	EnvKubeflowNamespace = "KUBEFLOW_NAMESPACE"
	// DefaultRestartPolicy is default RestartPolicy for ReplicaSpec.
	DefaultRestartPolicy = RestartPolicyNever
	// DefaultLauncherRestartPolicy is default RestartPolicy for Launcher Job.
	DefaultLauncherRestartPolicy = RestartPolicyOnFailure
	// OperatorName is the name of the operator used as value to the label common.OperatorLabelName
	OperatorName = "mpi-operator"
)

// merge from common.v1
// reference https://github.com/kubeflow/training-operator/blob/master/pkg/apis/kubeflow.org/v1/common_types.go
const (

	// ReplicaIndexLabel represents the label key for the replica-index, e.g. 0, 1, 2.. etc
	ReplicaIndexLabel = "training.kubeflow.org/replica-index"

	// ReplicaTypeLabel represents the label key for the replica-type, e.g. ps, worker etc.
	ReplicaTypeLabel = "training.kubeflow.org/replica-type"

	// OperatorNameLabel represents the label key for the operator name, e.g. tf-operator, mpi-operator, etc.
	OperatorNameLabel = "training.kubeflow.org/operator-name"

	// JobNameLabel represents the label key for the job name, the value is the job name.
	JobNameLabel = "training.kubeflow.org/job-name"

	// JobRoleLabel represents the label key for the job role, e.g. master.
	JobRoleLabel = "training.kubeflow.org/job-role"
)
