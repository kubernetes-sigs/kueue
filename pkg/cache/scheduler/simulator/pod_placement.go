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

package simulator

import "k8s.io/apimachinery/pkg/types"

type podPlacement struct {
	node           types.NodeName
	err            error
	failureReasons []string
}

// NewSuccessfulPlacement returns a PodPlacement that
// represents a binding of a Pod to a Node.
func NewSuccessfulPlacement(node types.NodeName) *podPlacement {
	return &podPlacement{
		node: node,
	}
}

// NewFailedPlacement returns a PodPlacement representing
// a Pod that failed to be scheduled.
func NewFailedPlacement(reasons ...string) *podPlacement {
	return &podPlacement{
		failureReasons: reasons,
	}
}

// NewPlacementError returns a PodPlacement representing
// a Pod that failed to be scheduled due to an explicit error.
func NewPlacementError(err error, reasons ...string) *podPlacement {
	return &podPlacement{
		err:            err,
		failureReasons: reasons,
	}
}

func (p *podPlacement) IsSuccess() bool {
	return p.err == nil && len(p.failureReasons) == 0 && p.node != ""
}

func (p *podPlacement) Node() types.NodeName {
	return p.node
}

func (p *podPlacement) IsError() bool {
	return p.err != nil
}

func (p *podPlacement) AsError() error {
	return p.err
}

func (p *podPlacement) Reasons() []string {
	return p.failureReasons
}
