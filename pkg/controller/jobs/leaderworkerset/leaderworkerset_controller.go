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

package leaderworkerset

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	gvk = leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet")
)

const (
	FrameworkName = "leaderworkerset.x-k8s.io/leaderworkerset"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:             SetupIndexes,
		NewReconciler:            NewReconciler,
		NewAdditionalReconcilers: []jobframework.ReconcilerFactory{NewPodReconciler},
		SetupWebhook:             SetupWebhook,
		JobType:                  &leaderworkersetv1.LeaderWorkerSet{},
		AddToScheme:              leaderworkersetv1.AddToScheme,
		DependencyList:           []string{"pod"},
		GVK:                      gvk,
	}))
}

type LeaderWorkerSet leaderworkersetv1.LeaderWorkerSet

func fromObject(o runtime.Object) *LeaderWorkerSet {
	return (*LeaderWorkerSet)(o.(*leaderworkersetv1.LeaderWorkerSet))
}

func (lws *LeaderWorkerSet) Object() client.Object {
	return (*leaderworkersetv1.LeaderWorkerSet)(lws)
}

func (lws *LeaderWorkerSet) GVK() schema.GroupVersionKind {
	return gvk
}

func SetupIndexes(context.Context, client.FieldIndexer) error {
	return nil
}
