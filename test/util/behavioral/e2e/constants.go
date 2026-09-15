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

package e2e

import (
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
)

// E2E-specific timeouts
const (
	Timeout         = 10 * time.Second
	MediumTimeout   = 45 * time.Second
	LongTimeout     = 90 * time.Second
	VeryLongTimeout = 5 * time.Minute
	Interval        = 250 * time.Millisecond
)

// Assertion helpers
var (
	IgnoreDeploymentConditionTimestampsAndMessage = cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "LastTransitionTime", "LastUpdateTime", "Message")
)

// Directory paths
var (
	ProjectBaseDir = getProjectBaseDir()
)

func getProjectBaseDir() string {
	// Implementation to get project base directory
	// Usually walks up from current directory to find go.mod
	return ""
}
