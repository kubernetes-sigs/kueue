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

package behavioral

import (
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Timeouts for waiting
const (
	TinyTimeout  = 10 * time.Millisecond
	ShortTimeout = time.Second
	Timeout      = 10 * time.Second
	// MediumTimeout is meant for tests when waiting for complex operations
	// such as running pods to completion.
	MediumTimeout = 45 * time.Second
	// LongTimeout is meant for tests when waiting for operations that
	// involve pod lifecycle transitions including container and sandbox teardown.
	LongTimeout = 90 * time.Second
	// VeryLongTimeout is meant for waiting for Kueue startup including
	// cert propagation and component readiness.
	VeryLongTimeout         = 5 * time.Minute
	ConsistentDuration      = 300 * time.Millisecond
	ShortConsistentDuration = 100 * time.Millisecond
	// LongConsistentDuration is for asserting that something does not happen
	// when a controller would take longer than ConsistentDuration to do it.
	LongConsistentDuration = 2 * time.Second
	ShortInterval          = 10 * time.Millisecond
	Interval               = time.Millisecond * 250
	LongInterval           = time.Second * 1
	// DRAExampleDriverName is the DeviceClass name registered by the dra-example-driver.
	DRAExampleDriverName = "gpu.example.com"
)

// Assertion helpers for conditions
var (
	IgnoreConditionTimestamps                                = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
	IgnoreConditionTimestampsAndObservedGeneration           = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "ObservedGeneration")
	IgnoreConditionMessage                                   = cmpopts.IgnoreFields(metav1.Condition{}, "Message")
	IgnoreObjectMetaResourceVersion                          = cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")
	IgnoreDeploymentConditionTimestampsAndMessage            = cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "LastTransitionTime", "LastUpdateTime", "Message")
	IgnorePodConditionTimestampsMessageAndObservedGeneration = cmpopts.IgnoreFields(corev1.PodCondition{}, "LastProbeTime", "LastTransitionTime", "Message", "ObservedGeneration")
)

// Pod behaviors for testing
var (
	// Starts a simple HTTP(S) with a few endpoints, one of which is the /exit endpoint which exits with `exit 0`
	BehaviorWaitForDeletion = []string{"netexec"}

	// Starts a container which always ends in failure on deletion.
	// To achieve this runs simple webserver, but does not register any signal handler.
	BehaviorWaitForDeletionFailOnExit = []string{"test-webserver"}

	// The agnhost container will print args passed and `exit 0`
	BehaviorExitFast = []string{"entrypoint-tester"}
)

// Shard names for testing
const (
	Shard0 = "shard-0"
	Shard1 = "shard-1"
)
