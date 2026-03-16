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

package util

import (
	"path/filepath"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
)

const (
	TinyTimeout  = 10 * time.Millisecond
	ShortTimeout = time.Second
	Timeout      = 10 * time.Second
	// MediumTimeout is meant for E2E tests when waiting for complex operations
	// such as running pods to completion.
	MediumTimeout = 45 * time.Second
	// LongTimeout is meant for E2E tests when waiting for operations that
	// involve pod lifecycle transitions including container and sandbox teardown.
	LongTimeout = 90 * time.Second
	// VeryLongTimeout is meant for waiting for Kueue startup including
	// cert propagation and component readiness.
	VeryLongTimeout         = 5 * time.Minute
	ConsistentDuration      = 1 * time.Second
	ShortConsistentDuration = 100 * time.Millisecond
	ShortInterval           = 10 * time.Millisecond
	Interval                = time.Millisecond * 250
	LongInterval            = time.Second * 1
)

var (
	IgnoreConditionTimestamps                                = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
	IgnoreConditionTimestampsAndObservedGeneration           = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "ObservedGeneration")
	IgnoreConditionMessage                                   = cmpopts.IgnoreFields(metav1.Condition{}, "Message")
	IgnoreObjectMetaResourceVersion                          = cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")
	IgnoreDeploymentConditionTimestampsAndMessage            = cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "LastTransitionTime", "LastUpdateTime", "Message")
	IgnorePodConditionTimestampsMessageAndObservedGeneration = cmpopts.IgnoreFields(corev1.PodCondition{}, "LastProbeTime", "LastTransitionTime", "Message", "ObservedGeneration")
)

var (
	ProjectBaseDir           = getProjectBaseDir()
	AutoscalerCrds           = filepath.Join(ProjectBaseDir, "dep-crds", "cluster-autoscaler")
	JobsetCrds               = filepath.Join(ProjectBaseDir, "dep-crds", "jobset-operator")
	TrainingOperatorCrds     = filepath.Join(ProjectBaseDir, "dep-crds", "training-operator-crds")
	KfTrainerCrds            = filepath.Join(ProjectBaseDir, "dep-crds", "kf-trainer-crds")
	KfTrainerClusterRuntimes = filepath.Join(ProjectBaseDir, "dep-crds", "kf-trainer-runtimes")
	MpiOperatorCrds          = filepath.Join(ProjectBaseDir, "dep-crds", "mpi-operator")
	AppWrapperCrds           = filepath.Join(ProjectBaseDir, "dep-crds", "appwrapper-crds")
	RayOperatorCrds          = filepath.Join(ProjectBaseDir, "dep-crds", "ray-operator-crds")
	SparkOperatorCrds        = filepath.Join(ProjectBaseDir, "dep-crds", "spark-operator-crds")
	WebhookPath              = filepath.Join(ProjectBaseDir, "config", "components", "webhook")
	ClusterProfileCrds       = filepath.Join(ProjectBaseDir, "dep-crds", "clusterprofile")
)

var (
	// For full documentation on agnhost subcommands see the following documentation:
	// https://pkg.go.dev/k8s.io/kubernetes/test/images/agnhost#section-readme

	// Starts a simple HTTP(S) with a few endpoints, one of which is the /exit endpoint which exits with `exit 0`
	BehaviorWaitForDeletion = []string{"netexec"}

	// Starts a container which always ends in failure on deletion.
	// To achieve this runs simple webserver, but does not register any signal handler.
	BehaviorWaitForDeletionFailOnExit = []string{"test-webserver"}

	// The agnhost container will print args passed and `exit 0`
	BehaviorExitFast = []string{"entrypoint-tester"}
)

var RealClock = clock.RealClock{}

// Validation error messages used in webhook tests
const (
	InvalidRFC1123Message  = `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`
	InvalidLabelKeyMessage = `name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')`
	InvalidPathMessage     = `Invalid path (regex used for validation is '[A-Za-z0-9/\-._~%!$&'()*+,;=:]+')`
)
