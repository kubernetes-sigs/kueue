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

package logging

import (
	"go.uber.org/zap/zapcore"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/util/slices"
)

// ObjectRefProvider is an interface for types that can provide a Kubernetes object reference.
type ObjectRefProvider interface {
	GetObject() client.Object
}

// GetObjectReferences converts a slice of ObjectRefProvider items to klog.ObjectRef slice.
// This is a generic utility that can work with any type that provides a Kubernetes object.
func GetObjectReferences[T ObjectRefProvider](items []T) []klog.ObjectRef {
	return slices.Map(items, func(item *T) klog.ObjectRef {
		return klog.KObj((*item).GetObject())
	})
}

// zapcore.Core that overrides log level of
// expected reconciler errors and emits them with
// defined target level.
// Other logs are left intact, and written using original core.
type ErrorLogLevelOverridenZapCore struct {
	zapcore.Core

	TargetLevel zapcore.Level
}

func (core ErrorLogLevelOverridenZapCore) Check(entry zapcore.Entry, checkedEntries *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.Enabled(entry.Level) {
		return checkedEntries.AddCore(entry, core)
	}
	return checkedEntries
}

func (core ErrorLogLevelOverridenZapCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	if entry.Level == zapcore.ErrorLevel && entry.Message == "Reconciler error" {
		entry.Level = core.TargetLevel
	}
	return core.Core.Write(entry, fields)
}

func (c ErrorLogLevelOverridenZapCore) With(fields []zapcore.Field) zapcore.Core {
	wrappedClone := c.Core.With(fields)
	clone := ErrorLogLevelOverridenZapCore{
		Core:        wrappedClone,
		TargetLevel: c.TargetLevel,
	}
	return clone
}

func NewErrorLogLevelOverridenCore(core zapcore.Core) zapcore.Core {
	return ErrorLogLevelOverridenZapCore{
		Core:        core,
		TargetLevel: zapcore.WarnLevel,
	}
}
