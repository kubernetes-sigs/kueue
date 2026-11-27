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

package handlers

import (
	"fmt"
	"net/url"
	"strconv"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	defaultDashboardPageSize = 25
	maxDashboardPageSize     = 100
)

type dashboardPagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	TotalCount int `json:"totalCount"`
}

func parseDashboardPagination(query url.Values) (*dashboardPagination, error) {
	if !query.Has("page") && !query.Has("pageSize") {
		return nil, nil
	}
	pagination := &dashboardPagination{PageSize: defaultDashboardPageSize}
	for _, field := range []struct {
		name  string
		value *int
	}{
		{name: "page", value: &pagination.Page},
		{name: "pageSize", value: &pagination.PageSize},
	} {
		values, present := query[field.name]
		if !present {
			continue
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("%s must be supplied once", field.name)
		}
		value, err := strconv.Atoi(values[0])
		if err != nil || value < 0 {
			return nil, fmt.Errorf("%s must be a non-negative integer", field.name)
		}
		*field.value = value
	}
	if pagination.PageSize < 1 || pagination.PageSize > maxDashboardPageSize {
		return nil, fmt.Errorf("pageSize must be between 1 and %d", maxDashboardPageSize)
	}
	return pagination, nil
}

// selectPage clamps before multiplying to avoid overflow and handle deletions
// that shrink the result while a client is subscribed to its last page.
func (p *dashboardPagination) selectPage(items []kueueapi.Workload) ([]kueueapi.Workload, *dashboardPagination) {
	info := *p
	info.TotalCount = len(items)
	lastPage := 0
	if len(items) > 0 {
		lastPage = (len(items) - 1) / info.PageSize
	}
	info.Page = min(info.Page, lastPage)
	start := info.Page * info.PageSize
	end := start + min(info.PageSize, len(items)-start)
	return items[start:end], &info
}
