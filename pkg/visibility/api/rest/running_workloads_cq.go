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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"

	_ "k8s.io/metrics/pkg/apis/metrics/install"
)

type runningWorkloadsInCqREST struct {
	c   *cache.Cache
	log logr.Logger
}

var _ rest.Storage = &runningWorkloadsInCqREST{}
var _ rest.GetterWithOptions = &runningWorkloadsInCqREST{}
var _ rest.Scoper = &runningWorkloadsInCqREST{}

func NewRunningWorkloadsInCqREST(c *cache.Cache) *runningWorkloadsInCqREST {
	return &runningWorkloadsInCqREST{
		c:   c,
		log: ctrl.Log.WithName("running-workload-in-cq"),
	}
}

// New implements rest.Storage interface
func (m *runningWorkloadsInCqREST) New() runtime.Object {
	return &v1alpha1.RunningWorkloadsSummary{}
}

// Destroy implements rest.Storage interface
func (m *runningWorkloadsInCqREST) Destroy() {}

// Get implements rest.GetterWithOptions interface
// It fetches information about running workloads and returns according to query params
func (m *runningWorkloadsInCqREST) Get(ctx context.Context, name string, opts runtime.Object) (runtime.Object, error) {
	runningWorkloadOpts, ok := opts.(*v1alpha1.RunningWorkloadOptions)
	if !ok {
		return nil, fmt.Errorf("invalid options object: %#v", opts)
	}
	limit := runningWorkloadOpts.Limit
	offset := runningWorkloadOpts.Offset

	wls := make([]v1alpha1.RunningWorkload, 0, limit)
	runningWorkloadsInfo, err := m.c.RunningWorkload(name)
	if err != nil {
		return nil, err
	}

	for index := int(offset); index < int(offset+limit) && index < len(runningWorkloadsInfo); index++ {
		wlInfo := runningWorkloadsInfo[index]
		wls = append(wls, *newRunningWorkload(wlInfo))
	}
	return &v1alpha1.RunningWorkloadsSummary{Items: wls}, nil
}

// NewGetOptions creates a new options object
func (m *runningWorkloadsInCqREST) NewGetOptions() (runtime.Object, bool, string) {
	// If no query parameters were passed the generated defaults function are not executed so it's necessary to set default values here as well
	return &v1alpha1.RunningWorkloadOptions{
		Limit: constants.DefaultRunningWorkloadsLimit,
	}, false, ""
}

// NamespaceScoped implements rest.Scoper interface
func (m *runningWorkloadsInCqREST) NamespaceScoped() bool {
	return false
}
