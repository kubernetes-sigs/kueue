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

package jobs

import (
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/controller/jobs/deployment"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	kubeflowjobs "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	"sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
)

// NewIntegrationManager creates an integration manager with all built-in job
// integrations registered.
func NewIntegrationManager() *jobframework.IntegrationManager {
	manager := jobframework.NewIntegrationManager()
	utilruntime.Must(RegisterIntegrations(manager))
	return manager
}

// RegisterIntegrations registers all built-in job integrations with manager.
func RegisterIntegrations(manager *jobframework.IntegrationManager) error {
	for _, register := range []func(*jobframework.IntegrationManager) error{
		appwrapper.RegisterIntegration,
		deployment.RegisterIntegration,
		job.RegisterIntegration,
		jobset.RegisterIntegration,
		kubeflowjobs.RegisterIntegrations,
		leaderworkerset.RegisterIntegration,
		mpijob.RegisterIntegration,
		pod.RegisterIntegration,
		raycluster.RegisterIntegration,
		rayjob.RegisterIntegration,
		rayservice.RegisterIntegration,
		sparkapplication.RegisterIntegration,
		statefulset.RegisterIntegration,
		trainjob.RegisterIntegration,
	} {
		if err := register(manager); err != nil {
			return err
		}
	}
	return nil
}
