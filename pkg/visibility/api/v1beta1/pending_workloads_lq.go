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

package v1beta1

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"

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
	return &visibility.PendingWorkloadsSummary{}
}

// Destroy implements rest.Storage interface
func (m *pendingWorkloadsInLqREST) Destroy() {}

// Get implements rest.GetterWithOptions interface
// It fetches information about pending workloads and returns according to query params
func (m *pendingWorkloadsInLqREST) Get(ctx context.Context, name string, opts runtime.Object) (runtime.Object, error) {
	pendingWorkloadOpts, ok := opts.(*visibility.PendingWorkloadOptions)
	if !ok {
		return nil, fmt.Errorf("invalid options object: %#v", opts)
	}
	limit := pendingWorkloadOpts.Limit
	offset := pendingWorkloadOpts.Offset

	namespace := genericapirequest.NamespaceValue(ctx)
	lqName := kueue.LocalQueueName(name)
	cqName, ok := m.queueMgr.ClusterQueueFromLocalQueue(utilqueue.NewLocalQueueReference(namespace, lqName))
	if !ok {
		return nil, errors.NewNotFound(visibility.Resource("localqueue"), name)
	}

	wls := make([]visibility.PendingWorkload, 0, limit)
	skippedWls := 0
	for index, wlInfo := range m.queueMgr.PendingWorkloadsInfo(cqName) {
		if len(wls) >= int(limit) {
			break
		}
		if wlInfo.Obj.Spec.QueueName == lqName {
			if skippedWls < int(offset) {
				skippedWls++
			} else {
				// Add a workload to results
				wls = append(wls, *newPendingWorkload(wlInfo, int32(len(wls)+int(offset)), index))
			}
		}
	}

	return &visibility.PendingWorkloadsSummary{Items: wls}, nil
}

// NewGetOptions creates a new options object
func (m *pendingWorkloadsInLqREST) NewGetOptions() (runtime.Object, bool, string) {
	// If no query parameters were passed the generated defaults function are not executed so it's necessary to set default values here as well
	return &visibility.PendingWorkloadOptions{
		Limit: constants.DefaultPendingWorkloadsLimit,
	}, false, ""
}

// NamespaceScoped implements rest.Scoper interface
func (m *pendingWorkloadsInLqREST) NamespaceScoped() bool {
	return true
}
