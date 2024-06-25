// Copyright 2021 The Kubeflow Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1

import (
	"fmt"

	log "github.com/sirupsen/logrus"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
)

// ValidateV1MXJob checks that the kubeflowv1.MXJobSpec is valid.
func ValidateV1MXJob(mxJob *MXJob) error {
	if errors := apimachineryvalidation.NameIsDNS1035Label(mxJob.ObjectMeta.Name, false); errors != nil {
		return fmt.Errorf("MXJob name is invalid: %v", errors)
	}
	if err := validateMXReplicaSpecs(mxJob.Spec.MXReplicaSpecs); err != nil {
		return err
	}
	return nil
}

// IsScheduler returns true if the type is Scheduler.
func IsScheduler(typ ReplicaType) bool {
	return typ == MXJobReplicaTypeScheduler
}

func validateMXReplicaSpecs(specs map[ReplicaType]*ReplicaSpec) error {
	if specs == nil {
		return fmt.Errorf("MXJobSpec is not valid")
	}
	foundScheduler := 0
	for rType, value := range specs {
		if value == nil || len(value.Template.Spec.Containers) == 0 {
			return fmt.Errorf("MXJobSpec is not valid")
		}
		if IsScheduler(rType) {
			foundScheduler++
		}
		// Make sure the image is defined in the container.
		numNamedMXNet := 0
		for _, container := range value.Template.Spec.Containers {
			if container.Image == "" {
				log.Warn("Image is undefined in the container")
				return fmt.Errorf("MXJobSpec is not valid")
			}
			if container.Name == MXJobDefaultContainerName {
				numNamedMXNet++
			}
		}
		// Make sure there has at least one container named "mxnet".
		if numNamedMXNet == 0 {
			log.Warnf("There is no container named mxnet in %v", rType)
			return fmt.Errorf("MXJobSpec is not valid")
		}
	}
	if foundScheduler > 1 {
		return fmt.Errorf("more than 1 scheduler found")
	}
	return nil
}
