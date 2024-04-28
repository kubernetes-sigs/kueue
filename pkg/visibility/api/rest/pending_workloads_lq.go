// Copyright 2023 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"

	_ "k8s.io/metrics/pkg/apis/metrics/install"
)

type pendingWorkloadsInLqREST struct {
	queueMgr *queue.Manager
	log      logr.Logger
}

var _ rest.Storage = &pendingWorkloadsInLqREST{}
var _ rest.GetterWithOptions = &pendingWorkloadsInLqREST{}
var _ rest.Scoper = &pendingWorkloadsInLqREST{}

func NewPendingWorkloadsInLqREST(kueueMgr *queue.Manager) *pendingWorkloadsInLqREST {
	return &pendingWorkloadsInLqREST{
		queueMgr: kueueMgr,
		log:      ctrl.Log.WithName("pending-workload-in-lq"),
	}
}

// New implements rest.Storage interface
func (m *pendingWorkloadsInLqREST) New() runtime.Object {
	return &v1alpha1.PendingWorkloadsSummary{}
}

// Destroy implements rest.Storage interface
func (m *pendingWorkloadsInLqREST) Destroy() {}

// Get implements rest.GetterWithOptions interface
// It fetches information about pending workloads and returns according to query params
func (m *pendingWorkloadsInLqREST) Get(ctx context.Context, name string, opts runtime.Object) (runtime.Object, error) {
	pendingWorkloadOpts, ok := opts.(*v1alpha1.PendingWorkloadOptions)
	if !ok {
		return nil, fmt.Errorf("invalid options object: %#v", opts)
	}
	limit := pendingWorkloadOpts.Limit
	offset := pendingWorkloadOpts.Offset

	namespace := genericapirequest.NamespaceValue(ctx)
	cqName, err := m.queueMgr.ClusterQueueFromLocalQueue(queue.QueueKey(namespace, name))
	if err != nil {
		return nil, errors.NewNotFound(v1alpha1.Resource("localqueue"), name)
	}

	wls := make([]v1alpha1.PendingWorkload, 0, limit)
	skippedWls := 0
	for index, wlInfo := range m.queueMgr.PendingWorkloadsInfo(cqName) {
		if len(wls) >= int(limit) {
			break
		}
		if wlInfo.Obj.Spec.QueueName == name {
			if skippedWls < int(offset) {
				skippedWls++
			} else {
				// Add a workload to results
				wls = append(wls, *newPendingWorkload(wlInfo, int32(len(wls)+int(offset)), index))
			}
		}
	}

	return &v1alpha1.PendingWorkloadsSummary{Items: wls}, nil
}

// NewGetOptions creates a new options object
func (m *pendingWorkloadsInLqREST) NewGetOptions() (runtime.Object, bool, string) {
	// If no query parameters were passed the generated defaults function are not executed so it's necessary to set default values here as well
	return &v1alpha1.PendingWorkloadOptions{
		Limit: constants.DefaultPendingWorkloadsLimit,
	}, false, ""
}

// NamespaceScoped implements rest.Scoper interface
func (m *pendingWorkloadsInLqREST) NamespaceScoped() bool {
	return true
}
