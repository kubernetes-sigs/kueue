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

package behavioral

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/onsi/gomega/format"
	"go.uber.org/zap/zaptest/observer"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	utillogging "sigs.k8s.io/kueue/pkg/util/logging"
)

func init() {
	// Use large MaxLength to make sure the diff contains relevant output
	format.MaxLength = 500000
	format.RegisterCustomFormatter(formatK8sObject)
}

func formatK8sObject(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	_, isObject := value.(runtime.Object)
	if !isObject && !isRuntimeObjectSlice(value) {
		return "", false
	}
	objYAML, err := yaml.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(objYAML), true
}

func isRuntimeObjectSlice(value any) bool {
	if value == nil {
		return false
	}
	val := reflect.ValueOf(value)
	if val.Kind() == reflect.Slice {
		k8sInterfaceType := reflect.TypeFor[runtime.Object]()
		sliceElemType := val.Type().Elem()
		if sliceElemType.Implements(k8sInterfaceType) || reflect.PointerTo(sliceElemType).Implements(k8sInterfaceType) {
			return true
		}
	}
	return false
}

func AssertMsg[T runtime.Object](message string, objs ...T) func() string {
	return func() string {
		var output strings.Builder
		fmt.Fprintln(&output, message)
		for _, obj := range objs {
			objYAML, ok := formatK8sObject(obj)
			if !ok {
				objYAML = format.Object(obj, 1)
			}
			fmt.Fprintln(&output, objYAML)
		}
		return output.String()
	}
}

func AssertMsgObjList(message string, list client.ObjectList) func() string {
	return func() string {
		items, err := apimeta.ExtractList(list)
		if err != nil {
			return fmt.Errorf("error during item extraction from list: %w", err).Error()
		}
		return AssertMsg(message, items...)()
	}
}

func IsLoggedEntryAConcurrentModification(le observer.LoggedEntry) bool {
	errLog, ok := le.ContextMap()["error"].(string)
	return ok && utillogging.IsWriteConflictError(errLog)
}
