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

package v1beta1

const (
	ResourceInUseFinalizerName                 = "kueue.x-k8s.io/resource-in-use"
	DefaultPodSetName          PodSetReference = "main"

	// ElasticJobSchedulingGate is the name of the scheduling gate applied to Pods
	// to delay their scheduling until the associated workload slice has been admitted.
	// This gate ensures that Pods do not begin scheduling prematurely, maintaining
	// proper sequencing in workload processing.
	ElasticJobSchedulingGate = "kueue.x-k8s.io/elastic-job"
)

type StopPolicy string

const (
	None         StopPolicy = "None"
	HoldAndDrain StopPolicy = "HoldAndDrain"
	Hold         StopPolicy = "Hold"
)
