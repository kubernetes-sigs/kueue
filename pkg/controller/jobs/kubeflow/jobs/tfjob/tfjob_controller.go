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

package tfjob

import (
	"context"
	"strings"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
)

var (
	gvk           = kftraining.SchemeGroupVersion.WithKind(kftraining.TFJobKind)
	FrameworkName = "kubeflow.org/tfjob"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupTFJobWebhook,
		JobType:                &kftraining.TFJob{},
		AddToScheme:            kftraining.AddToScheme,
		IsManagingObjectsOwner: isTFJob,
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=tfjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=tfjobs/status,verbs=get;update
// +kubebuilder:rbac:groups=kubeflow.org,resources=tfjobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

var NewReconciler = jobframework.NewGenericReconciler(func() jobframework.GenericJob {
	return &kubeflowjob.KubeflowJob{KFJobControl: (*JobControl)(&kftraining.TFJob{})}
}, nil, nil)

func isTFJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == kftraining.TFJobKind && strings.HasPrefix(owner.APIVersion, kftraining.SchemeGroupVersion.Group)
}

type JobControl kftraining.TFJob

var _ kubeflowjob.KFJobControl = (*JobControl)(nil)

func (j *JobControl) Object() client.Object {
	return (*kftraining.TFJob)(j)
}

func fromObject(o runtime.Object) *kubeflowjob.KubeflowJob {
	return &kubeflowjob.KubeflowJob{KFJobControl: (*JobControl)(o.(*kftraining.TFJob))}
}

func (j *JobControl) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *JobControl) RunPolicy() *kftraining.RunPolicy {
	return &j.Spec.RunPolicy
}

func (j *JobControl) ReplicaSpecs() map[kftraining.ReplicaType]*kftraining.ReplicaSpec {
	return j.Spec.TFReplicaSpecs
}

func (j *JobControl) JobStatus() *kftraining.JobStatus {
	return &j.Status
}

func (j *JobControl) OrderedReplicaTypes() []kftraining.ReplicaType {
	return []kftraining.ReplicaType{
		kftraining.TFJobReplicaTypeChief,
		kftraining.TFJobReplicaTypeMaster,
		kftraining.TFJobReplicaTypePS,
		kftraining.TFJobReplicaTypeWorker,
		kftraining.TFJobReplicaTypeEval,
	}
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForTFJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, gvk)
}
