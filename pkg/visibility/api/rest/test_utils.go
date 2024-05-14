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
	visibility "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
)

type pendingReq struct {
	nsName      string
	queueName   string
	queryParams *visibility.PendingWorkloadOptions
}

type pendingResp struct {
	wantErr              error
	wantPendingWorkloads []visibility.PendingWorkload
}

type runningReq struct {
	queueName   string
	queryParams *visibility.RunningWorkloadOptions
}

type runningResp struct {
	wantErr              error
	wantRunningWorkloads []visibility.RunningWorkload
}
