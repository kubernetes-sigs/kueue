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

package cache

import "sigs.k8s.io/kueue/pkg/hierarchy"

type CohortSnapshot struct {
	Name string

	ResourceNode ResourceNode
	hierarchy.Cohort[*ClusterQueueSnapshot, *CohortSnapshot]
}

func (c *CohortSnapshot) GetName() string {
	return c.Name
}

// The methods below implement hierarchicalResourceNode interface.

func (c *CohortSnapshot) getResourceNode() ResourceNode {
	return c.ResourceNode
}

func (c *CohortSnapshot) parentHRN() hierarchicalResourceNode {
	return c.Parent()
}
