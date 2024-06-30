// Copyright 2018 The Kubeflow Authors
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

func ValidateV1TFJob(tfjob *TFJob) error {
	if errors := apimachineryvalidation.NameIsDNS1035Label(tfjob.ObjectMeta.Name, false); errors != nil {
		return fmt.Errorf("TFJob name is invalid: %v", errors)
	}
	if err := validateV1TFReplicaSpecs(tfjob.Spec.TFReplicaSpecs); err != nil {
		return err
	}
	return nil
}

// IsChieforMaster returns true if the type is Master or Chief.
func IsChieforMaster(typ ReplicaType) bool {
	return typ == TFJobReplicaTypeChief || typ == TFJobReplicaTypeMaster
}

// IsWorker returns true if the type is Worker.
func IsWorker(typ ReplicaType) bool {
	return typ == TFJobReplicaTypeWorker
}

// IsEvaluator returns true if the type is Evaluator.
func IsEvaluator(typ ReplicaType) bool {
	return typ == TFJobReplicaTypeEval
}

func validateV1TFReplicaSpecs(specs map[ReplicaType]*ReplicaSpec) error {
	if specs == nil {
		return fmt.Errorf("TFJobSpec is not valid")
	}
	foundChief := 0
	for rType, value := range specs {
		if value == nil || len(value.Template.Spec.Containers) == 0 {
			return fmt.Errorf("TFJobSpec is not valid: containers definition expected in %v", rType)
		}
		if IsChieforMaster(rType) {
			foundChief++
		}
		// Make sure the image is defined in the container.
		numNamedTensorflow := 0
		for _, container := range value.Template.Spec.Containers {
			if container.Image == "" {
				msg := fmt.Sprintf("TFJobSpec is not valid: Image is undefined in the container of %v", rType)
				log.Error(msg)
				return fmt.Errorf(msg)
			}
			if container.Name == TFJobDefaultContainerName {
				numNamedTensorflow++
			}
		}
		// Make sure there has at least one container named "tensorflow".
		if numNamedTensorflow == 0 {
			msg := fmt.Sprintf("TFJobSpec is not valid: There is no container named %s in %v", TFJobDefaultContainerName, rType)
			log.Error(msg)
			return fmt.Errorf(msg)
		}
	}
	if foundChief > 1 {
		return fmt.Errorf("TFJobSpec is not valid: more than 1 chief/master found")
	}
	return nil
}
