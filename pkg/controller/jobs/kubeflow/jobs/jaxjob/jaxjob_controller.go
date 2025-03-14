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

package jaxjob

import (
	"context"
	"fmt"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
)

var (
	gvk           = kftraining.SchemeGroupVersion.WithKind(kftraining.JAXJobKind)
	FrameworkName = "kubeflow.org/jaxjob"

	SetupJaxJobWebhook = jobframework.BaseWebhookFactory(
		NewJob(),
		func(o runtime.Object) jobframework.GenericJob {
			return fromObject(o)
		},
	)
)

// +kubebuilder:webhook:path=/mutate-kubeflow-org-v1-jaxjob,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=jaxjobs,verbs=create,versions=v1,name=mjaxjob.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-kubeflow-org-v1-jaxjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=jaxjobs,verbs=create;update,versions=v1,name=vjaxjob.kb.io,admissionReviewVersions=v1

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupJaxJobWebhook,
		JobType:           &kftraining.JAXJob{},
		AddToScheme:       kftraining.AddToScheme,
		MultiKueueAdapter: kubeflowjob.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, fromObject),
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=jaxjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=jaxjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=jaxjobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &kubeflowjob.KubeflowJob{KFJobControl: &JobControl{}}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(func() jobframework.GenericJob {
	return &kubeflowjob.KubeflowJob{KFJobControl: &JobControl{}}
})

type JobControl kftraining.JAXJob

var _ kubeflowjob.KFJobControl = (*JobControl)(nil)

func (j *JobControl) Object() client.Object {
	return (*kftraining.JAXJob)(j)
}

func fromObject(o runtime.Object) *kubeflowjob.KubeflowJob {
	return &kubeflowjob.KubeflowJob{KFJobControl: (*JobControl)(o.(*kftraining.JAXJob))}
}

func (j *JobControl) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *JobControl) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s,%s=%s", kftraining.JobNameLabel, j.Name, kftraining.OperatorNameLabel, "jaxjob-controller")
}

func (j *JobControl) RunPolicy() *kftraining.RunPolicy {
	return &j.Spec.RunPolicy
}

func (j *JobControl) ReplicaSpecs() map[kftraining.ReplicaType]*kftraining.ReplicaSpec {
	return j.Spec.JAXReplicaSpecs
}

func (j *JobControl) ReplicaSpecsFieldName() string {
	return "jaxReplicaSpecs"
}

func (j *JobControl) JobStatus() *kftraining.JobStatus {
	return &j.Status
}

func (j *JobControl) OrderedReplicaTypes() []kftraining.ReplicaType {
	return []kftraining.ReplicaType{kftraining.JAXJobReplicaTypeWorker}
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForJAXJob(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}
