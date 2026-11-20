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

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apiserver/pkg/registry/generic/registry"
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

const (
	// errorContextKey is the field holding the error log values whose keys the
	// record already uses.
	errorContextKey = "errorContext"
	// errorContextTruncatedKey is the field reporting that the record can lack
	// log values of its errors.
	errorContextTruncatedKey = "errorContextTruncated"
)

// zapcore.Core that overrides log level of
// expected errors connected to concurrent resources modification and
// omits their stack trace.
// Those errors are emitted with klog V3 level.
// It also adds the log values carried by logged errors, see WithErrorValues.
// Other logs are left intact, and written using original core.
type CustomLogProcessor struct {
	zapcore.Core
	// withKeys holds the keys of the fields passed to With. The wrapped core
	// encodes those fields right away, so Write does not receive them.
	withKeys *fieldKeys
}

func (c CustomLogProcessor) Check(entry zapcore.Entry, checkedEntry *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checkedEntry.AddCore(entry, c)
	}
	return checkedEntry
}

func (c CustomLogProcessor) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	var (
		values        errorValues
		writeConflict bool
	)
	for i := range fields {
		if fields[i].Type != zapcore.ErrorType {
			continue
		}
		err, ok := fields[i].Interface.(error)
		if !ok || err == nil {
			continue
		}
		if entry.Level == zapcore.ErrorLevel && fields[i].Key == "error" && isWriteConflict(err) {
			writeConflict = true
		}
		values.collect(err, 0)
	}
	if writeConflict {
		entry.Level = klogV3Level
		entry.Stack = ""
	}
	if len(values.pairs) > 0 {
		fields = c.withErrorValues(fields, values)
	}
	return c.Core.Write(entry, fields)
}

const concurrentModificationErrorPrefix = "Operation cannot be fulfilled on"

func IsWriteConflictError(errMsg string) bool {
	// Error message sometimes is additionally prefixed like: "clearing admission: %w",
	// therefore, we use Contains instead of HasPrefix to also detect those messages.
	return strings.Contains(errMsg, concurrentModificationErrorPrefix) &&
		strings.Contains(errMsg, registry.OptimisticLockErrorMsg)
}

// isWriteConflict reports whether err is a concurrent modification error. It
// reports false if err.Error panics, as it usually does for an error that holds
// a nil pointer. zap recovers from that panic when it encodes the error.
func isWriteConflict(err error) (conflict bool) {
	defer func() {
		_ = recover()
	}()
	return IsWriteConflictError(err.Error())
}

// withErrorValues returns fields followed by the fields for values. A value is
// logged under its own key, unless the record already uses that key: then it is
// logged under errorContextKey, so that neither value replaces the other. A
// value whose key is taken there too is dropped, which
// errorContextTruncatedKey reports.
func (c CustomLogProcessor) withErrorValues(fields []zapcore.Field, values errorValues) []zapcore.Field {
	var top, nested []logValue
	truncated := values.truncated
	for _, pair := range values.pairs {
		switch {
		case !isReservedKey(pair.key) && !c.recordHasKey(fields, pair.key) && !containsKey(top, pair.key):
			top = append(top, pair)
		case !containsKey(nested, pair.key):
			nested = append(nested, logValue{key: pair.key, value: nestedValue(pair.value)})
		default:
			truncated = true
		}
	}
	extra := zapFields(top)
	if len(nested) > 0 {
		if c.recordHasKey(fields, errorContextKey) {
			truncated = true
		} else {
			nestedFields := zapFields(nested)
			extra = append(extra, zap.Object(errorContextKey, zapcore.ObjectMarshalerFunc(func(enc zapcore.ObjectEncoder) error {
				for _, field := range nestedFields {
					field.AddTo(enc)
				}
				return nil
			})))
		}
	}
	if truncated && !c.recordHasKey(fields, errorContextTruncatedKey) {
		extra = append(extra, zap.Bool(errorContextTruncatedKey, true))
	}
	// Clip, so that append copies the fields instead of writing into an array
	// that the caller may share.
	return append(arrayslices.Clip(fields), extra...)
}

// recordHasKey reports whether the record made of fields, or the logger that
// writes it, already has a field named key.
func (c CustomLogProcessor) recordHasKey(fields []zapcore.Field, key string) bool {
	return c.withKeys.contains(key) || arrayslices.ContainsFunc(fields, func(field zapcore.Field) bool {
		return field.Key == key && field.Type != zapcore.SkipType
	})
}

// isReservedKey reports whether key is written by the encoder or by
// CustomLogProcessor. The encoder keys are those of the production encoder
// config of zap, which controller-runtime uses, and those that zap derives from
// the "error" field.
func isReservedKey(key string) bool {
	switch key {
	case "ts", "level", "logger", "caller", "msg", "stacktrace",
		"error", "errorVerbose", "errorCauses", "errorError",
		errorContextKey, errorContextTruncatedKey:
		return true
	}
	return false
}

// nestedValue returns the value to log for value inside an object. For a field
// of the record, the encoder of controller-runtime logs an API object as its
// kind and name. It does not do that inside an object, where the whole API
// object would be logged, so log a client.Object as a reference instead.
func nestedValue(value any) any {
	if obj, ok := value.(client.Object); ok {
		return klog.KObj(obj)
	}
	return value
}

// zapFields converts pairs the way zapr converts the key-value pairs of a log
// call. CustomLogProcessor runs below zapr, where zap alone would log a value
// like klog.KObj as a string instead of an object.
func zapFields(pairs []logValue) []zapcore.Field {
	if len(pairs) == 0 {
		return nil
	}
	keysAndValues := make([]any, 0, 2*len(pairs))
	for _, pair := range pairs {
		keysAndValues = append(keysAndValues, pair.key, pair.value)
	}
	collector := &fieldCollector{Core: zapcore.NewNopCore()}
	zapr.NewLoggerWithOptions(zap.New(collector), zapr.DPanicOnBugs(false)).WithValues(keysAndValues...)
	return collector.fields
}

// fieldCollector is a zapcore.Core that records the fields passed to With.
type fieldCollector struct {
	zapcore.Core
	fields []zapcore.Field
}

func (c *fieldCollector) With(fields []zapcore.Field) zapcore.Core {
	c.fields = append(c.fields, fields...)
	return c
}

// fieldKeys is an immutable list of field keys. A core derived by With shares
// the keys of its parent.
type fieldKeys struct {
	parent *fieldKeys
	keys   []string
}

func (k *fieldKeys) with(fields []zapcore.Field) *fieldKeys {
	keys := make([]string, 0, len(fields))
	for i := range fields {
		if fields[i].Type != zapcore.SkipType {
			keys = append(keys, fields[i].Key)
		}
	}
	if len(keys) == 0 {
		return k
	}
	return &fieldKeys{parent: k, keys: keys}
}

func (k *fieldKeys) contains(key string) bool {
	for ; k != nil; k = k.parent {
		if arrayslices.Contains(k.keys, key) {
			return true
		}
	}
	return false
}

func (c CustomLogProcessor) With(fields []zapcore.Field) zapcore.Core {
	wrappedClone := c.Core.With(fields)
	return CustomLogProcessor{
		Core:     wrappedClone,
		withKeys: c.withKeys.with(fields),
	}
}

func NewCustomLogProcessor(core zapcore.Core) zapcore.Core {
	return CustomLogProcessor{
		Core: core,
	}
}
