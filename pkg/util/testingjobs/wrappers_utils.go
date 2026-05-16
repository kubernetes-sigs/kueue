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

package testingjobs

const (
	// TestDefaultContainerImage is an intentionally non-existing image used as the
	// default in test wrappers. It ensures e2e tests fail fast (ImagePullBackOff)
	// if a real image is not explicitly set, while having no effect on unit or
	// integration tests that never pull images.
	TestDefaultContainerImage = "registry.k8s.io/pause:non-existing"
)
