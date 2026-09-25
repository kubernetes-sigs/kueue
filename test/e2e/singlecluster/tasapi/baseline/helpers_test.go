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

package baseline

const (
	instanceType      = "tas-group"
	tasNodeGroupLabel = "cloud.provider.com/node-group"
	extraResource     = "example.com/gpu"
)

// blockOfNode maps each e2e TAS cluster node to the topology block it
// belongs to (see hack/testing/kind-cluster-tas.yaml): kind-worker through
// kind-worker4 are in block "b1", kind-worker5 through kind-worker8 are in
// block "b2".
var blockOfNode = map[string]string{
	"kind-worker":  "b1",
	"kind-worker2": "b1",
	"kind-worker3": "b1",
	"kind-worker4": "b1",
	"kind-worker5": "b2",
	"kind-worker6": "b2",
	"kind-worker7": "b2",
	"kind-worker8": "b2",
}
