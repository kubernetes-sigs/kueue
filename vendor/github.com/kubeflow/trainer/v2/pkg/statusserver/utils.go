/*
Copyright 2026 The Kubeflow Authors.

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

package statusserver

import "fmt"

const (
	// TokenAudiencePrefix is the prefix for projected service account token audiences
	TokenAudiencePrefix = "trainer.kubeflow.org"
)

// TokenAudience returns the required audience for a TrainJob's status endpoint.
func TokenAudience(namespace, name string) string {
	return fmt.Sprintf("%s/v1alpha1/namespaces/%s/trainjobs/%s/status", TokenAudiencePrefix, namespace, name)
}

// StatusUrl is the path of the endpoint for receiving status updates
func StatusUrl(namespace, name string) string {
	return fmt.Sprintf("/apis/trainer.kubeflow.org/v1alpha1/namespaces/%s/trainjobs/%s/status", namespace, name)
}
