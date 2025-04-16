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

package appwrapper

import (
	"context"
	"fmt"
	"strconv"

	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	awutils "github.com/project-codeflare/appwrapper/pkg/utils"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)

	FrameworkName = "workload.codeflare.dev/appwrapper"

	NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

	SetupAppWrapperWebhook = jobframework.BaseWebhookFactory(
		NewJob(),
		func(o runtime.Object) jobframework.GenericJob {
			return fromObject(o)
		},
	)
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		NewJob:            NewJob,
		GVK:               gvk,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupAppWrapperWebhook,
		JobType:           &awv1beta2.AppWrapper{},
		SetupIndexes:      SetupIndexes,
		AddToScheme:       awv1beta2.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers/finalizers,verbs=get;update
//+kubebuilder:webhook:path=/mutate-workload-codeflare-dev-v1beta2-appwrapper,mutating=true,failurePolicy=fail,sideEffects=None,groups=workload.codeflare.dev,resources=appwrappers,verbs=create,versions=v1beta2,name=mappwrapper.kb.io,admissionReviewVersions=v1
//+kubebuilder:webhook:path=/validate-workload-codeflare-dev-v1beta2-appwrapper,mutating=false,failurePolicy=fail,sideEffects=None,groups=workload.codeflare.dev,resources=appwrappers,verbs=create;update,versions=v1beta2,name=vappwrapper.kb.io,admissionReviewVersions=v1

func NewJob() jobframework.GenericJob {
	return &AppWrapper{}
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

type AppWrapper awv1beta2.AppWrapper

var _ jobframework.GenericJob = (*AppWrapper)(nil)
var _ jobframework.JobWithManagedBy = (*AppWrapper)(nil)

func fromObject(o runtime.Object) *AppWrapper {
	return (*AppWrapper)(o.(*awv1beta2.AppWrapper))
}

func (j *AppWrapper) Object() client.Object {
	return (*awv1beta2.AppWrapper)(j)
}

func (j *AppWrapper) IsSuspended() bool {
	return j.Spec.Suspend
}

func (j *AppWrapper) IsActive() bool {
	return meta.IsStatusConditionTrue(j.Status.Conditions, string(awv1beta2.QuotaReserved))
}

func (j *AppWrapper) Suspend() {
	j.Spec.Suspend = true
}

func (j *AppWrapper) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *AppWrapper) PodSets() ([]kueue.PodSet, error) {
	podSpecTemplates, awPodSets, err := awutils.GetComponentPodSpecs((*awv1beta2.AppWrapper)(j))
	if err != nil {
		ctrl.Log.Error(err, "Error returned from awutils.GetComponentPodSpecs", "appwrapper", j)
		return nil, err
	}
	podSets := make([]kueue.PodSet, len(podSpecTemplates))
	for psIndex := range podSpecTemplates {
		var podIndexLabel *string
		var subGroupIndexLabel *string
		var subGroupCount *int32
		if annotation, ok := awPodSets[psIndex].Annotations[awutils.PodSetAnnotationTASPodIndexLabel]; ok {
			podIndexLabel = &annotation
		}
		if annotation, ok := awPodSets[psIndex].Annotations[awutils.PodSetAnnotationTASSubGroupIndexLabel]; ok {
			subGroupIndexLabel = &annotation
		}
		if annotation, ok := awPodSets[psIndex].Annotations[awutils.PodSetAnnotationTASSubGroupCount]; ok {
			if count, err := strconv.Atoi(annotation); err == nil {
				subGroupCount = ptr.To(int32(count))
			} else {
				ctrl.Log.Error(err, "Malformed annotation ignored",
					"annotationKey", awutils.PodSetAnnotationTASSubGroupCount,
					"annotationValue", annotation)
			}
		}
		podSets[psIndex] = kueue.PodSet{
			Name:     kueue.NewPodSetReference(fmt.Sprintf("%s-%v", j.Name, psIndex)),
			Template: *podSpecTemplates[psIndex],
			Count:    awutils.Replicas(awPodSets[psIndex]),
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			podSets[psIndex].TopologyRequest = jobframework.PodSetTopologyRequest(
				&(podSpecTemplates[psIndex].ObjectMeta),
				podIndexLabel,
				subGroupIndexLabel,
				subGroupCount,
			)
		}
	}
	return podSets, nil
}

func (j *AppWrapper) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	awPodSetsInfo := make([]awv1beta2.AppWrapperPodSetInfo, len(podSetsInfo))
	for idx := range podSetsInfo {
		awPodSetsInfo[idx].Annotations = podSetsInfo[idx].Annotations
		awPodSetsInfo[idx].Labels = podSetsInfo[idx].Labels
		awPodSetsInfo[idx].NodeSelector = podSetsInfo[idx].NodeSelector
		awPodSetsInfo[idx].Tolerations = podSetsInfo[idx].Tolerations
		awPodSetsInfo[idx].SchedulingGates = podSetsInfo[idx].SchedulingGates
	}

	if err := awutils.SetPodSetInfos((*awv1beta2.AppWrapper)(j), awPodSetsInfo); err != nil {
		return fmt.Errorf("%w: %w", podset.ErrInvalidPodsetInfo, err)
	}
	j.Spec.Suspend = false
	return nil
}

func (j *AppWrapper) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	return awutils.ClearPodSetInfos((*awv1beta2.AppWrapper)(j))
}

func (j *AppWrapper) Finished() (message string, success, finished bool) {
	switch j.Status.Phase {
	case awv1beta2.AppWrapperSucceeded:
		return "AppWrapper finished successfully", true, true

	case awv1beta2.AppWrapperFailed:
		if meta.IsStatusConditionTrue(j.Status.Conditions, string(awv1beta2.ResourcesDeployed)) {
			return "Still deleting resources for failed AppWrapper", false, false
		} else {
			return "AppWrapper failed", false, true
		}
	}
	return "", false, false
}

func (j *AppWrapper) PodsReady() bool {
	return meta.IsStatusConditionTrue(j.Status.Conditions, string(awv1beta2.PodsReady))
}

func (j *AppWrapper) CanDefaultManagedBy() bool {
	jobSpecManagedBy := j.Spec.ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(jobSpecManagedBy == nil || *jobSpecManagedBy == awv1beta2.AppWrapperControllerName)
}

func (j *AppWrapper) ManagedBy() *string {
	return j.Spec.ManagedBy
}

func (j *AppWrapper) SetManagedBy(managedBy *string) {
	j.Spec.ManagedBy = managedBy
}

func GetWorkloadNameForAppWrapper(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}
