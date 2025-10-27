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

package manager

import (
	"k8s.io/client-go/rest"
)

// SetGetConfigForTest allows tests to override the kube config provider.
// This function is intended for testing purposes only and should not be used in release code.
func SetGetConfigForTest(fn func() *rest.Config) (prev func() *rest.Config) {
	prev = getConfig
	getConfig = fn
	return prev
}
