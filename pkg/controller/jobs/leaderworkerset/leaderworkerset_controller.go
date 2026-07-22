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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	gvk = leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet")
)

const (
	FrameworkName = "leaderworkerset.x-k8s.io/leaderworkerset"
	// defaultLeaderWorkerSetReplicas mirrors the LeaderWorkerSet API default for
	// spec.replicas (+kubebuilder:default=1), and is used when the field is unset.
	defaultLeaderWorkerSetReplicas = 1
	// maxLeaderWorkerSetReplicas is a sanity upper bound on spec.replicas that guards
	// against unbounded per-replica Workload creation from an unreasonably large value.
	maxLeaderWorkerSetReplicas = 1_000_000
)

var errInvalidLeaderWorkerSetReplicas = errors.New("invalid LeaderWorkerSet replicas")

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:                    SetupIndexes,
		NewReconciler:                   NewReconciler,
		SetupWebhook:                    SetupWebhook,
		JobType:                         &leaderworkersetv1.LeaderWorkerSet{},
		AddToScheme:                     leaderworkersetv1.AddToScheme,
		ImplicitlyEnabledFrameworkNames: []string{"pod"},
		GVK:                             gvk,
		MultiKueueAdapter:               &multiKueueAdapter{},
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

func validateLeaderWorkerSetReplicaCount(replicas int32) error {
	if replicas < 0 {
		return fmt.Errorf("%w: must be >= 0, got %d", errInvalidLeaderWorkerSetReplicas, replicas)
	}
	if replicas > maxLeaderWorkerSetReplicas {
		return fmt.Errorf("%w: must be <= %d, got %d", errInvalidLeaderWorkerSetReplicas, maxLeaderWorkerSetReplicas, replicas)
	}
	return nil
}

// GetOwnerUID returns the UID to use for workload name calculation.
// In MultiKueue remote scenarios, it returns the origin UID from the annotation.
func GetOwnerUID(lws *leaderworkersetv1.LeaderWorkerSet) types.UID {
	if _, isMultiKueueRemote := lws.Labels[kueue.MultiKueueOriginLabel]; isMultiKueueRemote {
		if originUID, ok := lws.Annotations[kueue.MultiKueueOriginUIDAnnotation]; ok {
			return types.UID(originUID)
		}
	}
	return lws.UID
}
