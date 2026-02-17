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
	// LongTimeout is meant for E2E tests when waiting for complex operations
	// such as running pods to completion.
	LongTimeout = 45 * time.Second
	// VeryLongTimeout is meant for E2E tests involving Ray which starts ray-project images (over 2GB)
	// and also synchronizes the cluster before it can be used
	VeryLongTimeout = 5 * time.Minute
	// StartUpTimeout is meant to be used for waiting for Kueue to startup, given
	// that cert updates can take up to 3 minutes to propagate to the filesystem.
	// Taken into account that after the certificates are ready, all Kueue's components
	// need started and the time it takes for a change in ready probe response triggers
	// a change in the deployment status.
	StartUpTimeout          = 5 * time.Minute
	ShortConsistentDuration = 10 * time.Millisecond
	ConsistentDuration      = 1 * time.Second
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
