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

package util

import (
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	TinyTimeout  = 10 * time.Millisecond
	ShortTimeout = time.Second
	Timeout      = 5 * time.Second
	// LongTimeout is meant for E2E tests when waiting for complex operations
	// such as running pods to completion.
	LongTimeout = 45 * time.Second
	// StartUpTimeout is meant to be used for waiting for Kueue to startup, given
	// that cert updates can take up to 3 minutes to propagate to the filesystem.
	// Taken into account that after the certificates are ready, all Kueue's components
	// need started and the time it takes for a change in ready probe response triggers
	// a change in the deployment status.
	StartUpTimeout     = 5 * time.Minute
	ConsistentDuration = time.Second
	Interval           = time.Millisecond * 250
)

var (
	IgnoreConditionTimestamps                      = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
	IgnoreConditionTimestampsAndObservedGeneration = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "ObservedGeneration")
	IgnoreConditionMessage                         = cmpopts.IgnoreFields(metav1.Condition{}, "Message")
	IgnoreObjectMetaResourceVersion                = cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")
)
