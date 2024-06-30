// Copyright 2022 The Kubeflow Authors
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

	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
)

func ValidateV1PaddleJob(paddleJob *PaddleJob) error {
	if errors := apimachineryvalidation.NameIsDNS1035Label(paddleJob.ObjectMeta.Name, false); errors != nil {
		return fmt.Errorf("PaddleJob name is invalid: %v", errors)
	}
	if err := validatePaddleReplicaSpecs(paddleJob.Spec.PaddleReplicaSpecs); err != nil {
		return err
	}
	return nil
}

func validatePaddleReplicaSpecs(specs map[ReplicaType]*ReplicaSpec) error {
	if specs == nil {
		return fmt.Errorf("PaddleJobSpec is not valid")
	}
	for rType, value := range specs {
		if value == nil || len(value.Template.Spec.Containers) == 0 {
			return fmt.Errorf("PaddleJobSpec is not valid: containers definition expected in %v", rType)
		}
		// Make sure the replica type is valid.
		validReplicaTypes := []ReplicaType{PaddleJobReplicaTypeMaster, PaddleJobReplicaTypeWorker}

		isValidReplicaType := false
		for _, t := range validReplicaTypes {
			if t == rType {
				isValidReplicaType = true
				break
			}
		}

		if !isValidReplicaType {
			return fmt.Errorf("PaddleReplicaType is %v but must be one of %v", rType, validReplicaTypes)
		}

		//Make sure the image is defined in the container
		defaultContainerPresent := false
		for _, container := range value.Template.Spec.Containers {
			if container.Image == "" {
				msg := fmt.Sprintf("PaddleJobSpec is not valid: Image is undefined in the container of %v", rType)
				return fmt.Errorf(msg)
			}
			if container.Name == PaddleJobDefaultContainerName {
				defaultContainerPresent = true
			}
		}
		//Make sure there has at least one container named "paddle"
		if !defaultContainerPresent {
			msg := fmt.Sprintf("PaddleJobSpec is not valid: There is no container named %s in %v", PaddleJobDefaultContainerName, rType)
			return fmt.Errorf(msg)
		}

	}

	return nil
}
