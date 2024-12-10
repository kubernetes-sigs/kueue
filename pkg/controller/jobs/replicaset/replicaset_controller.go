/*
Copyright 2024 The Kubernetes Authors.

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

package replicaset

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	gvk = appsv1.SchemeGroupVersion.WithKind("ReplicaSet")
)

const (
	FrameworkName = "replicaset"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewReconciler:          jobframework.NewNoopReconcilerFactory(gvk),
		GVK:                    gvk,
		SetupWebhook:           SetupWebhook,
		JobType:                &appsv1.ReplicaSet{},
		AddToScheme:            appsv1.AddToScheme,
		DependencyList:         []string{"pod"},
		IsManagingObjectsOwner: isReplicaSet,
	}))
}

type ReplicaSet appsv1.ReplicaSet

func fromObject(o runtime.Object) *ReplicaSet {
	return (*ReplicaSet)(o.(*appsv1.ReplicaSet))
}

func (rs *ReplicaSet) Object() client.Object {
	return (*appsv1.ReplicaSet)(rs)
}

func (d *ReplicaSet) GVK() schema.GroupVersionKind {
	return gvk
}

func SetupIndexes(context.Context, client.FieldIndexer) error {
	return nil
}

func isReplicaSet(owner *metav1.OwnerReference) bool {
	return owner.Kind == "ReplicaSet" && owner.APIVersion == gvk.GroupVersion().String()
}
