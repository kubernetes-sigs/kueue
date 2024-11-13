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

package statefulset

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	gvk = appsv1.SchemeGroupVersion.WithKind("StatefulSet")
)

const (
	FrameworkName = "statefulset"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:   SetupIndexes,
		NewReconciler:  NewReconciler,
		SetupWebhook:   SetupWebhook,
		JobType:        &appsv1.StatefulSet{},
		AddToScheme:    appsv1.AddToScheme,
		DependencyList: []string{"pod"},
		GVK:            gvk,
	}))
}

type StatefulSet appsv1.StatefulSet

func fromObject(o runtime.Object) *StatefulSet {
	return (*StatefulSet)(o.(*appsv1.StatefulSet))
}

func (d *StatefulSet) Object() client.Object {
	return (*appsv1.StatefulSet)(d)
}

func (d *StatefulSet) GVK() schema.GroupVersionKind {
	return gvk
}

func SetupIndexes(context.Context, client.FieldIndexer) error {
	return nil
}
