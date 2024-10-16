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

package v1beta1

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"

	_ "k8s.io/metrics/pkg/apis/metrics/install"
)

type pendingWorkloadsInCqREST struct {
	queueMgr *queue.Manager
	log      logr.Logger
}

var _ rest.Storage = &pendingWorkloadsInCqREST{}
var _ rest.GetterWithOptions = &pendingWorkloadsInCqREST{}
var _ rest.Scoper = &pendingWorkloadsInCqREST{}

func NewPendingWorkloadsInCqREST(kueueMgr *queue.Manager) *pendingWorkloadsInCqREST {
	return &pendingWorkloadsInCqREST{
		queueMgr: kueueMgr,
		log:      ctrl.Log.WithName("pending-workload-in-cq"),
	}
}

// New implements rest.Storage interface
func (m *pendingWorkloadsInCqREST) New() runtime.Object {
	return &visibility.PendingWorkloadsSummary{}
}

// Destroy implements rest.Storage interface
func (m *pendingWorkloadsInCqREST) Destroy() {}

// Get implements rest.GetterWithOptions interface
// It fetches information about pending workloads and returns according to query params
func (m *pendingWorkloadsInCqREST) Get(_ context.Context, name string, opts runtime.Object) (runtime.Object, error) {
	pendingWorkloadOpts, ok := opts.(*visibility.PendingWorkloadOptions)
	if !ok {
		return nil, fmt.Errorf("invalid options object: %#v", opts)
	}
	limit := pendingWorkloadOpts.Limit
	offset := pendingWorkloadOpts.Offset

	wls := make([]visibility.PendingWorkload, 0, limit)
	pendingWorkloadsInfo := m.queueMgr.PendingWorkloadsInfo(name)
	if pendingWorkloadsInfo == nil {
		return nil, errors.NewNotFound(visibility.Resource("clusterqueue"), name)
	}

	localQueuePositions := make(map[string]int32, 0)

	for index := 0; index < int(offset+limit) && index < len(pendingWorkloadsInfo); index++ {
		// Update positions in LocalQueue
		wlInfo := pendingWorkloadsInfo[index]
		queueName := wlInfo.Obj.Spec.QueueName
		positionInLocalQueue := localQueuePositions[queueName]
		localQueuePositions[queueName]++

		if index >= int(offset) {
			// Add a workload to results
			wls = append(wls, *newPendingWorkload(wlInfo, positionInLocalQueue, index))
		}
	}
	return &visibility.PendingWorkloadsSummary{Items: wls}, nil
}

// NewGetOptions creates a new options object
func (m *pendingWorkloadsInCqREST) NewGetOptions() (runtime.Object, bool, string) {
	// If no query parameters were passed the generated defaults function are not executed so it's necessary to set default values here as well
	return &visibility.PendingWorkloadOptions{
		Limit: constants.DefaultPendingWorkloadsLimit,
	}, false, ""
}

// NamespaceScoped implements rest.Scoper interface
func (m *pendingWorkloadsInCqREST) NamespaceScoped() bool {
	return false
}
