/*
Copyright 2023 The Kubernetes Authors.

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

// Reference the job framework integration packages to ensure linking.
import (
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/deployment"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
)
