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
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterQueueStatusApplyConfiguration represents an declarative configuration of the ClusterQueueStatus type for use
// with apply.
type ClusterQueueStatusApplyConfiguration struct {
	FlavorsReservation     []FlavorUsageApplyConfiguration                       `json:"flavorsReservation,omitempty"`
	FlavorsUsage           []FlavorUsageApplyConfiguration                       `json:"flavorUsage,omitempty"`
	PendingWorkloads       *int32                                                `json:"pendingWorkloads,omitempty"`
	ReservingdWorkloads    *int32                                                `json:"reservingdWorkloads,omitempty"`
	AdmittedWorkloads      *int32                                                `json:"admittedWorkloads,omitempty"`
	Conditions             []v1.Condition                                        `json:"conditions,omitempty"`
	PendingWorkloadsStatus *ClusterQueuePendingWorkloadsStatusApplyConfiguration `json:"pendingWorkloadsStatus,omitempty"`
}

// ClusterQueueStatusApplyConfiguration constructs an declarative configuration of the ClusterQueueStatus type for use with
// apply.
func ClusterQueueStatus() *ClusterQueueStatusApplyConfiguration {
	return &ClusterQueueStatusApplyConfiguration{}
}

// WithFlavorsReservation adds the given value to the FlavorsReservation field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the FlavorsReservation field.
func (b *ClusterQueueStatusApplyConfiguration) WithFlavorsReservation(values ...*FlavorUsageApplyConfiguration) *ClusterQueueStatusApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithFlavorsReservation")
		}
		b.FlavorsReservation = append(b.FlavorsReservation, *values[i])
	}
	return b
}

// WithFlavorsUsage adds the given value to the FlavorsUsage field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the FlavorsUsage field.
func (b *ClusterQueueStatusApplyConfiguration) WithFlavorsUsage(values ...*FlavorUsageApplyConfiguration) *ClusterQueueStatusApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithFlavorsUsage")
		}
		b.FlavorsUsage = append(b.FlavorsUsage, *values[i])
	}
	return b
}

// WithPendingWorkloads sets the PendingWorkloads field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the PendingWorkloads field is set to the value of the last call.
func (b *ClusterQueueStatusApplyConfiguration) WithPendingWorkloads(value int32) *ClusterQueueStatusApplyConfiguration {
	b.PendingWorkloads = &value
	return b
}

// WithReservingdWorkloads sets the ReservingdWorkloads field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ReservingdWorkloads field is set to the value of the last call.
func (b *ClusterQueueStatusApplyConfiguration) WithReservingdWorkloads(value int32) *ClusterQueueStatusApplyConfiguration {
	b.ReservingdWorkloads = &value
	return b
}

// WithAdmittedWorkloads sets the AdmittedWorkloads field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the AdmittedWorkloads field is set to the value of the last call.
func (b *ClusterQueueStatusApplyConfiguration) WithAdmittedWorkloads(value int32) *ClusterQueueStatusApplyConfiguration {
	b.AdmittedWorkloads = &value
	return b
}

// WithConditions adds the given value to the Conditions field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Conditions field.
func (b *ClusterQueueStatusApplyConfiguration) WithConditions(values ...v1.Condition) *ClusterQueueStatusApplyConfiguration {
	for i := range values {
		b.Conditions = append(b.Conditions, values[i])
	}
	return b
}

// WithPendingWorkloadsStatus sets the PendingWorkloadsStatus field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the PendingWorkloadsStatus field is set to the value of the last call.
func (b *ClusterQueueStatusApplyConfiguration) WithPendingWorkloadsStatus(value *ClusterQueuePendingWorkloadsStatusApplyConfiguration) *ClusterQueueStatusApplyConfiguration {
	b.PendingWorkloadsStatus = value
	return b
}
