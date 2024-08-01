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

package provisioning

const (
	ConfigKind             = "ProvisioningRequestConfig"
	ControllerName         = "kueue.x-k8s.io/provisioning-request"
	ConsumesAnnotationKey  = "autoscaling.x-k8s.io/consume-provisioning-request"
	ClassNameAnnotationKey = "autoscaling.x-k8s.io/provisioning-class-name"

	CheckInactiveMessage = "the check is not active"
	NoRequestNeeded      = "the provisioning request is not needed"
)
