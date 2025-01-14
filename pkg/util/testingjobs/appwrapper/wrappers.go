/*
Copyright 2025 The Kubernetes Authors.

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

package appwrapper

import (
	"encoding/json"
	"strings"

	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// AppWrapperWrapper wraps an AppWrapper.
type AppWrapperWrapper struct {
	awv1beta2.AppWrapper
}

// MakeAppWrapper creates a wrapper for a suspended AppWrapper with no components.
func MakeAppWrapper(name string, ns string) *AppWrapperWrapper {
	return &AppWrapperWrapper{
		AppWrapper: awv1beta2.AppWrapper{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   ns,
				Annotations: make(map[string]string, 1),
			},
			Spec: awv1beta2.AppWrapperSpec{
				Suspend:    true,
				Components: []awv1beta2.AppWrapperComponent{},
			},
		},
	}
}

// Obj returns the inner AppWrapper.
func (aw *AppWrapperWrapper) Obj() *awv1beta2.AppWrapper {
	return &aw.AppWrapper
}

// Label sets a label of the AppWrapper
func (aw *AppWrapperWrapper) Label(k, v string) *AppWrapperWrapper {
	if aw.Labels == nil {
		aw.Labels = make(map[string]string)
	}
	aw.Labels[k] = v
	return aw
}

// Annotations updates annotations of the AppWrapper.
func (aw *AppWrapperWrapper) Annotations(annotations map[string]string) *AppWrapperWrapper {
	aw.ObjectMeta.Annotations = annotations
	return aw
}

// Queue updates the queue name of the AppWrapper
func (aw *AppWrapperWrapper) Queue(q string) *AppWrapperWrapper {
	return aw.Label(constants.QueueLabel, q)
}

// Name updates the name of the AppWrapper
func (aw *AppWrapperWrapper) Name(n string) *AppWrapperWrapper {
	aw.ObjectMeta.Name = n
	return aw
}

// Component adds a component to the AppWrapper
func (aw *AppWrapperWrapper) Component(comp runtime.Object) *AppWrapperWrapper {
	data, err := json.Marshal(comp)
	if err == nil {
		// See https://github.com/project-codeflare/codeflare-operator/pull/630
		// The root cause is that the Kubernetes API defines creationTimestamp as a struct instead of a pointer
		patchedData := strings.ReplaceAll(string(data), `"metadata":{"creationTimestamp":null},`, "")
		patchedData = strings.ReplaceAll(patchedData, `"metadata":{"creationTimestamp":null,`, `"metadata":{`)
		patchedData = strings.ReplaceAll(patchedData, `"creationTimestamp":null,`, "")
		awc := awv1beta2.AppWrapperComponent{
			Template: runtime.RawExtension{
				Raw: []byte(patchedData),
			},
		}
		aw.AppWrapper.Spec.Components = append(aw.AppWrapper.Spec.Components, awc)
	}
	return aw
}

// ComponentWithInfos adds a component to the AppWrapper
func (aw *AppWrapperWrapper) ComponentWithInfos(comp runtime.Object, infos ...awv1beta2.AppWrapperPodSetInfo) *AppWrapperWrapper {
	data, err := json.Marshal(comp)
	if err == nil {
		// See https://github.com/project-codeflare/codeflare-operator/pull/630
		// The root cause is that the Kubernetes API defines creationTimestamp as a struct instead of a pointer
		patchedData := strings.ReplaceAll(string(data), `"metadata":{"creationTimestamp":null},`, "")
		patchedData = strings.ReplaceAll(patchedData, `"metadata":{"creationTimestamp":null,`, `"metadata":{`)
		patchedData = strings.ReplaceAll(patchedData, `"creationTimestamp":null,`, "")
		awc := awv1beta2.AppWrapperComponent{
			Template: runtime.RawExtension{
				Raw: []byte(patchedData),
			},
			PodSetInfos: infos,
		}
		aw.AppWrapper.Spec.Components = append(aw.AppWrapper.Spec.Components, awc)
	}
	return aw
}

// Suspend updates the suspend status of the AppWrapper.
func (aw *AppWrapperWrapper) Suspend(s bool) *AppWrapperWrapper {
	aw.Spec.Suspend = s
	return aw
}

// StatusCondition sets a status condition of the AppWrapper.
func (aw *AppWrapperWrapper) SetCondition(condition metav1.Condition) *AppWrapperWrapper {
	meta.SetStatusCondition(&aw.Status.Conditions, condition)
	return aw
}

// SetPhase sets the status phase of the AppWrapeer.
func (aw *AppWrapperWrapper) SetPhase(phase awv1beta2.AppWrapperPhase) *AppWrapperWrapper {
	aw.AppWrapper.Status.Phase = phase
	return aw
}
