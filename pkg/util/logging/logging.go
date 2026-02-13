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
	arrayslices "slices"
	"strings"
	"unicode"

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

// as indicated in https://pkg.go.dev/github.com/go-logr/zapr#hdr-Usage
// logr log levels correspond to custom zapcore levels and
// zapLevel = -1*logrLevel, so we set it to -3 as it means first verbosity
// not visible by users in default settings,
// see https://github.com/kubernetes/community/blob/88841374e9558803b5b2ec81beb450e246283f09/contributors/devel/sig-instrumentation/logging.md?plain=1#L109
const klogV3Level = zapcore.Level(-3)

// zapcore.Core that overrides log level of
// expected errors connected to concurrent resources modification and
// omits their stack trace.
// Those errors are emitted with klog V3 level.
// Other logs are left intact, and written using original core.
type CustomLogProcessor struct {
	zapcore.Core
}

func (core CustomLogProcessor) Check(entry zapcore.Entry, checkedEntry *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.Enabled(entry.Level) {
		return checkedEntry.AddCore(entry, core)
	}
	return checkedEntry
}

func (core CustomLogProcessor) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	if isEntryAConcurrentModificationError(entry, fields) {
		entry.Level = klogV3Level
		entry.Stack = ""
	}
	return core.Core.Write(entry, fields)
}

const concurrentModificationErrorSuffix = "the object has been modified; please apply your changes to the latest version and try again"
const concurrentModificationErrorPrefix = "Operation cannot be fulfilled on"

func isEntryAConcurrentModificationError(entry zapcore.Entry, fields []zapcore.Field) bool {
	if entry.Level != zapcore.ErrorLevel {
		return false
	}
	return arrayslices.ContainsFunc(fields, func(field zapcore.Field) bool {
		if field.Key != "error" || field.Type != zapcore.ErrorType {
			return false
		}
		err := field.Interface.(error)
		if err == nil {
			return false
		}
		errorDescription := strings.TrimFunc(err.Error(), unicode.IsSpace)
		// Error messages sometimes are additionally prefixed like: "clearing admission: %w"
		// therefore we use Contains instead of HasPrefix to also detect those messages
		return strings.Contains(errorDescription, concurrentModificationErrorPrefix) && strings.HasSuffix(errorDescription, concurrentModificationErrorSuffix)
	})
}

func (c CustomLogProcessor) With(fields []zapcore.Field) zapcore.Core {
	wrappedClone := c.Core.With(fields)
	return CustomLogProcessor{
		Core: wrappedClone,
	}
}

func NewCustomLogProcessor(core zapcore.Core) zapcore.Core {
	return CustomLogProcessor{
		Core: core,
	}
}
