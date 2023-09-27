/*
Copyright 2022 The Kubernetes Authors.

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
// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1beta1

import (
	v1 "k8s.io/api/core/v1"
)

// PodSetApplyConfiguration represents an declarative configuration of the PodSet type for use
// with apply.
type PodSetApplyConfiguration struct {
	Name            *string             `json:"name,omitempty"`
	Template        *v1.PodTemplateSpec `json:"template,omitempty"`
	Count           *int32              `json:"count,omitempty"`
	MinCount        *int32              `json:"minCount,omitempty"`
	PodTemplateName *string             `json:"podTemplateName,omitempty"`
}

// PodSetApplyConfiguration constructs an declarative configuration of the PodSet type for use with
// apply.
func PodSet() *PodSetApplyConfiguration {
	return &PodSetApplyConfiguration{}
}

// WithName sets the Name field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Name field is set to the value of the last call.
func (b *PodSetApplyConfiguration) WithName(value string) *PodSetApplyConfiguration {
	b.Name = &value
	return b
}

// WithTemplate sets the Template field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Template field is set to the value of the last call.
func (b *PodSetApplyConfiguration) WithTemplate(value v1.PodTemplateSpec) *PodSetApplyConfiguration {
	b.Template = &value
	return b
}

// WithCount sets the Count field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Count field is set to the value of the last call.
func (b *PodSetApplyConfiguration) WithCount(value int32) *PodSetApplyConfiguration {
	b.Count = &value
	return b
}

// WithMinCount sets the MinCount field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the MinCount field is set to the value of the last call.
func (b *PodSetApplyConfiguration) WithMinCount(value int32) *PodSetApplyConfiguration {
	b.MinCount = &value
	return b
}

// WithPodTemplateName sets the PodTemplateName field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the PodTemplateName field is set to the value of the last call.
func (b *PodSetApplyConfiguration) WithPodTemplateName(value string) *PodSetApplyConfiguration {
	b.PodTemplateName = &value
	return b
}
