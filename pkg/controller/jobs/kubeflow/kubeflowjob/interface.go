/*
Copyright 2023 The Kubernetes Authors.

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

package kubeflowjob

import (
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type KFJobControl interface {
	// Object returns the KFJob interface.
	Object() client.Object
	// GVK returns the GroupVersionKind for the KFJob.
	GVK() schema.GroupVersionKind
	// RunPolicy returns the RunPolicy for the KFJob.
	RunPolicy() *kftraining.RunPolicy
	// ReplicaSpecs returns the ReplicaSpecs for the KFJob.
	ReplicaSpecs() map[kftraining.ReplicaType]*kftraining.ReplicaSpec
	// ReplicaSpecsFieldName returns the field name of the ReplicaSpecs.
	ReplicaSpecsFieldName() string
	// JobStatus returns the JobStatus for the KFJob.
	JobStatus() *kftraining.JobStatus
	// OrderedReplicaTypes returns the ordered list of ReplicaTypes for the KFJob.
	OrderedReplicaTypes() []kftraining.ReplicaType
}
