/*
Copyright 2025 The Kubernetes Authors.

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
	"strings"

	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	awutils "github.com/project-codeflare/appwrapper/pkg/utils"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = awv1beta2.GroupVersion.WithKind("AppWrapper")

	FrameworkName = "codeflare.dev/appwrapper"

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
		NewJob:                 NewJob,
		GVK:                    gvk,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupAppWrapperWebhook,
		JobType:                &awv1beta2.AppWrapper{},
		SetupIndexes:           SetupIndexes,
		AddToScheme:            awv1beta2.AddToScheme,
		IsManagingObjectsOwner: isAppWrapper,
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workload.codeflare.dev,resources=appwrappers/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &AppWrapper{}
}

func isAppWrapper(owner *metav1.OwnerReference) bool {
	return owner.Kind == "AppWrapper" && strings.HasPrefix(owner.APIVersion, gvk.Group)
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

type AppWrapper awv1beta2.AppWrapper

var _ jobframework.GenericJob = (*AppWrapper)(nil)

func fromObject(o runtime.Object) *AppWrapper {
	return (*AppWrapper)(o.(*awv1beta2.AppWrapper))
}

func (aw *AppWrapper) Object() client.Object {
	return (*awv1beta2.AppWrapper)(aw)
}

func (aw *AppWrapper) IsSuspended() bool {
	return aw.Spec.Suspend
}

func (aw *AppWrapper) IsActive() bool {
	return meta.IsStatusConditionTrue(aw.Status.Conditions, string(awv1beta2.QuotaReserved))
}

func (aw *AppWrapper) Suspend() {
	aw.Spec.Suspend = true
}

func (aw *AppWrapper) GVK() schema.GroupVersionKind {
	return gvk
}

func (aw *AppWrapper) PodSets() ([]kueue.PodSet, error) {
	podSets, err := awutils.GetPodSets((*awv1beta2.AppWrapper)(aw))
	if err != nil {
		ctrl.Log.Error(err, "Error returned from awutils.GetPodSets", "appwrapper", aw)
		return nil, err
	}
	return podSets, nil
}

func (aw *AppWrapper) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	if err := awutils.SetPodSetInfos((*awv1beta2.AppWrapper)(aw), podSetsInfo); err != nil {
		return err
	}
	aw.Spec.Suspend = false
	return nil
}

func (aw *AppWrapper) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	return awutils.ClearPodSetInfos((*awv1beta2.AppWrapper)(aw))
}

func (aw *AppWrapper) Finished() (message string, success, finished bool) {
	switch aw.Status.Phase {
	case awv1beta2.AppWrapperSucceeded:
		return "AppWrapper finished successfully", true, true

	case awv1beta2.AppWrapperFailed:
		if meta.IsStatusConditionTrue(aw.Status.Conditions, string(awv1beta2.ResourcesDeployed)) {
			return "Still deleting resources for failed AppWrapper", false, false
		} else {
			return "AppWrapper failed", false, true
		}
	}
	return "", false, false
}

func (aw *AppWrapper) PodsReady() bool {
	return meta.IsStatusConditionTrue(aw.Status.Conditions, string(awv1beta2.PodsReady))
}

func GetWorkloadNameForAppWrapper(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}
