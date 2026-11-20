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
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	gocmp "github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
)

// custom debug levels used by logr to better align with usage
const infoLevel = zapcore.Level(-4)
const warningLevel = zapcore.Level(-3)

type observedLog struct {
	message string
	level   zapcore.Level
	fields  []zap.Field
}

func TestCustomLogProcessor(t *testing.T) {
	core, observedLogs := observer.New(infoLevel)
	logger := zap.New(NewCustomLogProcessor(core))

	const reconcilerErrorMessage = "Reconciler error"
	const concurrentModificationErrorDetails = "Operation cannot be fulfilled on workloads.kueue.x-k8s.io \"job-job2-907f9\": the object has been modified; please apply your changes to the latest version and try again"
	concurrentModificationErrorDetailsField := newErrorField(concurrentModificationErrorDetails)
	concurrentModificationErrorDetailsFieldWithCustomPrefixAndSuffix := newErrorField(
		fmt.Sprintf("clearing admission: %v after some other error: some other error", concurrentModificationErrorDetails),
	)

	someOtherField := zap.String("Some field", "Some value")
	someOtherErrorMessage := "This is another test error connected with concurrent modification"
	otherErrorDetailsField := newErrorField("Some other error details")

	testCases := map[string]struct {
		logCall     func()
		expectedLog observedLog
	}{
		"Changes error level of reconciler error with concurrent modification": {
			logCall: func() {
				logger.Error(reconcilerErrorMessage, concurrentModificationErrorDetailsField)
			},
			expectedLog: observedLog{
				reconcilerErrorMessage, warningLevel, []zap.Field{concurrentModificationErrorDetailsField},
			},
		},
		"Changes error level of child logger created by With": {
			logCall: func() {
				childLogger := logger.With(someOtherField)
				childLogger.Error(reconcilerErrorMessage, concurrentModificationErrorDetailsField)
			}, expectedLog: observedLog{reconcilerErrorMessage, warningLevel, []zap.Field{concurrentModificationErrorDetailsField, someOtherField}},
		},
		"Changes error level of non reconciler error with concurrent modification": {
			logCall: func() {
				logger.Error(someOtherErrorMessage, concurrentModificationErrorDetailsField)
			},
			expectedLog: observedLog{someOtherErrorMessage, warningLevel, []zap.Field{concurrentModificationErrorDetailsField}},
		},
		"Changes error level of concurrent modification log with custom prefix and suffix": {
			logCall: func() {
				logger.Error(someOtherErrorMessage, concurrentModificationErrorDetailsFieldWithCustomPrefixAndSuffix)
			},
			expectedLog: observedLog{someOtherErrorMessage, warningLevel, []zap.Field{concurrentModificationErrorDetailsFieldWithCustomPrefixAndSuffix}},
		},
		"Does not change error level of non concurrent modification error (without appropriate error details)": {
			logCall: func() {
				logger.Error(someOtherErrorMessage)
			},
			expectedLog: observedLog{someOtherErrorMessage, zapcore.ErrorLevel, []zap.Field{}},
		},
		"Does not change error level of non concurrent modification error (with other error details)": {
			logCall: func() {
				logger.Error(reconcilerErrorMessage, otherErrorDetailsField)
			},
			expectedLog: observedLog{reconcilerErrorMessage, zapcore.ErrorLevel, []zap.Field{otherErrorDetailsField}},
		},
		"Does not change logging level of concurrent modification error with info log level": {
			logCall: func() {
				logger.Log(infoLevel, reconcilerErrorMessage, concurrentModificationErrorDetailsField)
			},
			expectedLog: observedLog{reconcilerErrorMessage, infoLevel, []zap.Field{concurrentModificationErrorDetailsField}},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tc.logCall()

			logs := observedLogs.TakeAll()
			if len(logs) != 1 {
				t.Errorf("Unexpected number of log entries %v, expected 1\n", len(logs))
			}

			log := logs[0]
			if log.Message != tc.expectedLog.message || log.Level != tc.expectedLog.level {
				t.Errorf("Unexpected log entry %v\n", log)
			}

			context := sortFieldsByKeys(log.Context)
			expectedFields := sortFieldsByKeys(tc.expectedLog.fields)

			if !reflect.DeepEqual(context, expectedFields) {
				t.Errorf("Unexpected field values %v, expected %v\n", context, expectedFields)
			}
		})
	}
}

type DummyError struct {
	errorDetails string
}

func (e DummyError) Error() string {
	return e.errorDetails
}

func newErrorField(errorDetailsValue string) zap.Field {
	return zap.Error(DummyError{errorDetailsValue})
}

func sortFieldsByKeys(fields []zap.Field) []zap.Field {
	return slices.SortedFunc(slices.Values(fields), func(a, b zap.Field) int {
		return cmp.Compare(a.Key, b.Key)
	})
}

// nilPointerError panics in Error when the receiver is nil.
type nilPointerError struct {
	msg string
}

func (e *nilPointerError) Error() string {
	return e.msg
}

// selfJoinedError wraps itself twice, so its error tree has no end.
type selfJoinedError struct{}

func (e *selfJoinedError) Error() string {
	return "self joined"
}

func (e *selfJoinedError) Unwrap() []error {
	return []error{e, e}
}

func (e *selfJoinedError) LogValues() []any {
	return []any{"job", "a"}
}

// panickingUnwrapError panics when its error chain is read.
type panickingUnwrapError struct {
	msg string
}

func (e panickingUnwrapError) Error() string {
	return e.msg
}

func (e panickingUnwrapError) Unwrap() error {
	panic("unwrap failed")
}

// panickingLogValuesError panics when its log values are read.
type panickingLogValuesError struct {
	err error
}

func (e panickingLogValuesError) Error() string {
	return e.err.Error()
}

func (e panickingLogValuesError) Unwrap() error {
	return e.err
}

func (e panickingLogValuesError) LogValues() []any {
	panic("log values failed")
}

func TestCustomLogProcessorErrorValues(t *testing.T) {
	const writeConflictMessage = "Operation cannot be fulfilled on workloads.kueue.x-k8s.io \"wl\": " +
		"the object has been modified; please apply your changes to the latest version and try again"

	errLookup := errors.New("lookup failed")
	errOther := errors.New("other failure")
	parentJob := klog.KRef("ns", "parent")
	workload := klog.KRef("ns", "wl")
	otherWorkload := klog.KRef("ns", "wl-other")
	pod := &corev1.Pod{Namespace: "ns", Name: "pod"}

	manyValues := make([]any, 0, 2*(maxErrorValues+1))
	wantManyValues := map[string]any{
		"error":                  errLookup.Error(),
		errorContextTruncatedKey: true,
	}
	for i := range maxErrorValues + 1 {
		key := fmt.Sprintf("key%02d", i)
		manyValues = append(manyValues, key, "value")
		if i < maxErrorValues {
			wantManyValues[key] = "value"
		}
	}

	// The log call sets the truncation key itself.
	wantManyValuesWithCallKey := maps.Clone(wantManyValues)
	wantManyValuesWithCallKey[errorContextTruncatedKey] = "from-call"

	// The values of the innermost error are deeper than the limit.
	deepErr := WithErrorValues(errLookup, "workload", workload)
	for range maxErrorDepth + 1 {
		deepErr = fmt.Errorf("wrapped: %w", deepErr)
	}

	testCases := map[string]struct {
		logCall     func(log logr.Logger)
		wantLevel   zapcore.Level
		wantContext map[string]any
	}{
		"error without values is logged as before": {
			logCall: func(log logr.Logger) {
				log.Error(fmt.Errorf("reconciling: %w", errLookup), "Reconciler error", "job", "a")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"job":   "a",
				"error": "reconciling: lookup failed",
			},
		},
		"values of a wrapped error are logged": {
			logCall: func(log logr.Logger) {
				err := WithErrorValues(errLookup, "ancestorJob", parentJob, "reason", "timeout")
				log.Error(fmt.Errorf("reconciling: %w", err), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":       "reconciling: lookup failed",
				"ancestorJob": parentJob.MarshalLog(),
				"reason":      "timeout",
			},
		},
		"values of every error in a chain are logged": {
			logCall: func(log logr.Logger) {
				inner := WithErrorValues(errLookup, "workload", workload)
				outer := WithErrorValues(fmt.Errorf("suspending: %w", inner), "ancestorJob", parentJob)
				log.Error(fmt.Errorf("reconciling: %w", outer), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":       "reconciling: suspending: lookup failed",
				"ancestorJob": parentJob.MarshalLog(),
				"workload":    workload.MarshalLog(),
			},
		},
		"values of joined errors are logged": {
			logCall: func(log logr.Logger) {
				err := errors.Join(
					WithErrorValues(errLookup, "clusterQueue", "cq"),
					fmt.Errorf("updating: %w", WithErrorValues(errOther, "resourceFlavor", "rf")),
				)
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":          "lookup failed\nupdating: other failure",
				"clusterQueue":   "cq",
				"resourceFlavor": "rf",
			},
		},
		"values of aggregated errors are logged": {
			logCall: func(log logr.Logger) {
				err := utilerrors.NewAggregate([]error{
					WithErrorValues(errLookup, "clusterQueue", "cq"),
					WithErrorValues(errOther, "resourceFlavor", "rf"),
				})
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "[lookup failed, other failure]",
				// zap adds this field for an error that lists its causes.
				"errorCauses": []any{
					map[string]any{"error": "lookup failed"},
					map[string]any{"error": "other failure"},
				},
				"clusterQueue":   "cq",
				"resourceFlavor": "rf",
			},
		},
		"values of an error logged at info level are logged": {
			logCall: func(log logr.Logger) {
				log.V(3).Info("Ignored the error", "error", WithErrorValues(errLookup, "ancestorJob", parentJob))
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"error":       "lookup failed",
				"ancestorJob": parentJob.MarshalLog(),
			},
		},
		"values of an error logged under another key are logged": {
			logCall: func(log logr.Logger) {
				log.V(3).Info("Ignored the error", "cause", WithErrorValues(errLookup, "ancestorJob", parentJob))
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"cause":       "lookup failed",
				"ancestorJob": parentJob.MarshalLog(),
			},
		},
		"concurrent modification error keeps its values": {
			logCall: func(log logr.Logger) {
				log.Error(WithErrorValues(errors.New(writeConflictMessage), "ancestorJob", parentJob), "Reconciler error")
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"error":       writeConflictMessage,
				"ancestorJob": parentJob.MarshalLog(),
			},
		},
		"key of the log call is kept": {
			logCall: func(log logr.Logger) {
				log.Error(WithErrorValues(errLookup, "job", "from-error"), "Reconciler error", "job", "from-call")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"job":           "from-call",
				"error":         "lookup failed",
				errorContextKey: map[string]any{"job": "from-error"},
			},
		},
		"key of the logger is kept": {
			logCall: func(log logr.Logger) {
				log = log.WithValues("namespace", "ns").WithValues("name", "child")
				log.Error(WithErrorValues(errLookup, "namespace", "other-ns", "name", "other"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"namespace":     "ns",
				"name":          "child",
				"error":         "lookup failed",
				errorContextKey: map[string]any{"namespace": "other-ns", "name": "other"},
			},
		},
		"reserved keys are kept": {
			logCall: func(log logr.Logger) {
				err := WithErrorValues(errLookup,
					"ts", 1, "level", 2, "logger", 3, "caller", 4, "msg", 5, "stacktrace", 6,
					"error", 7, "errorVerbose", 8, "errorCauses", 9, "errorError", 10,
					errorContextKey, 11, errorContextTruncatedKey, 12)
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "lookup failed",
				errorContextKey: map[string]any{
					"ts": int64(1), "level": int64(2), "logger": int64(3), "caller": int64(4), "msg": int64(5), "stacktrace": int64(6),
					"error": int64(7), "errorVerbose": int64(8), "errorCauses": int64(9), "errorError": int64(10),
					errorContextKey: int64(11), errorContextTruncatedKey: int64(12),
				},
			},
		},
		"outer error keeps a key that an inner error also uses": {
			logCall: func(log logr.Logger) {
				inner := WithErrorValues(errLookup, "workload", otherWorkload)
				outer := WithErrorValues(fmt.Errorf("suspending: %w", inner), "workload", workload)
				log.Error(outer, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":         "suspending: lookup failed",
				"workload":      workload.MarshalLog(),
				errorContextKey: map[string]any{"workload": otherWorkload.MarshalLog()},
			},
		},
		"equal values of one key in an error chain are logged once": {
			logCall: func(log logr.Logger) {
				inner := WithErrorValues(errLookup, "workload", workload)
				outer := WithErrorValues(fmt.Errorf("suspending: %w", inner), "workload", workload)
				log.WithValues("workload", workload).Error(outer, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":         "suspending: lookup failed",
				"workload":      workload.MarshalLog(),
				errorContextKey: map[string]any{"workload": workload.MarshalLog()},
			},
		},
		"third value of a key is dropped and reported": {
			logCall: func(log logr.Logger) {
				err := errors.Join(
					WithErrorValues(errLookup, "workload", workload),
					WithErrorValues(errOther, "workload", otherWorkload),
					WithErrorValues(errOther, "workload", parentJob),
				)
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  "lookup failed\nother failure\nother failure",
				"workload":               workload.MarshalLog(),
				errorContextKey:          map[string]any{"workload": otherWorkload.MarshalLog()},
				errorContextTruncatedKey: true,
			},
		},
		"error context key of the log call is kept": {
			logCall: func(log logr.Logger) {
				log.Error(WithErrorValues(errLookup, "job", "from-error"), "Reconciler error",
					"job", "from-call", errorContextKey, "from-call")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"job":                    "from-call",
				errorContextKey:          "from-call",
				"error":                  "lookup failed",
				errorContextTruncatedKey: true,
			},
		},
		"API object in the error context is logged by reference": {
			logCall: func(log logr.Logger) {
				log.Error(WithErrorValues(errLookup, "pod", pod), "Reconciler error", "pod", "from-call")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"pod":           "from-call",
				"error":         "lookup failed",
				errorContextKey: map[string]any{"pod": klog.KObj(pod).MarshalLog()},
			},
		},
		"key without a value is ignored": {
			logCall: func(log logr.Logger) {
				// Passed as a slice, because loggercheck rejects such a call.
				keysAndValues := []any{"job", "a", "queue"}
				log.Error(WithErrorValues(errLookup, keysAndValues...), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "lookup failed",
				"job":   "a",
			},
		},
		"values from the first key that is not a string are ignored": {
			logCall: func(log logr.Logger) {
				keysAndValues := []any{"job", "a", 1, "b", "queue", "c"}
				log.Error(WithErrorValues(errLookup, keysAndValues...), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "lookup failed",
				"job":   "a",
			},
		},
		"values beyond the limit are dropped and reported": {
			logCall: func(log logr.Logger) {
				log.Error(WithErrorValues(errLookup, manyValues...), "Reconciler error")
			},
			wantLevel:   zapcore.ErrorLevel,
			wantContext: wantManyValues,
		},
		"error context key of the logger is kept": {
			logCall: func(log logr.Logger) {
				log = log.WithValues("job", "from-logger", errorContextKey, "from-logger")
				log.Error(WithErrorValues(errLookup, "job", "from-error"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"job":                    "from-logger",
				errorContextKey:          "from-logger",
				"error":                  "lookup failed",
				errorContextTruncatedKey: true,
			},
		},
		"truncation key of the log call is kept": {
			logCall: func(log logr.Logger) {
				log.Error(WithErrorValues(errLookup, manyValues...), "Reconciler error", errorContextTruncatedKey, "from-call")
			},
			wantLevel:   zapcore.ErrorLevel,
			wantContext: wantManyValuesWithCallKey,
		},
		"values of other errors are kept when reading one error panics": {
			logCall: func(log logr.Logger) {
				err := errors.Join(
					panickingUnwrapError{msg: "unwrap"},
					WithErrorValues(errLookup, "clusterQueue", "cq"),
				)
				log.V(3).Info("Ignored the error", "cause", err)
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"cause":                  "unwrap\nlookup failed",
				"clusterQueue":           "cq",
				errorContextTruncatedKey: true,
			},
		},
		"values of wrapped errors are kept when reading the values of an error panics": {
			logCall: func(log logr.Logger) {
				err := panickingLogValuesError{err: WithErrorValues(errLookup, "clusterQueue", "cq")}
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  "lookup failed",
				"clusterQueue":           "cq",
				errorContextTruncatedKey: true,
			},
		},
		"values deeper than the limit are not read": {
			logCall: func(log logr.Logger) {
				log.Error(WithErrorValues(deepErr, "job", "a"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  deepErr.Error(),
				"job":                    "a",
				errorContextTruncatedKey: true,
			},
		},
		"error that contains itself is read up to the limit": {
			logCall: func(log logr.Logger) {
				log.Error(&selfJoinedError{}, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  "self joined",
				"job":                    "a",
				errorContextTruncatedKey: true,
			},
		},
		"concurrent modification error is relabeled when reading its values panics": {
			logCall: func(log logr.Logger) {
				log.Error(panickingUnwrapError{msg: writeConflictMessage}, "Reconciler error")
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"error": writeConflictMessage,
			},
		},
		"error holding a nil pointer does not panic": {
			logCall: func(log logr.Logger) {
				var err *nilPointerError
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "<nil>",
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			core, observedLogs := observer.New(infoLevel)
			tc.logCall(zapr.NewLogger(zap.New(NewCustomLogProcessor(core))))

			logs := observedLogs.TakeAll()
			if len(logs) != 1 {
				t.Fatalf("Unexpected number of log entries %d, expected 1", len(logs))
			}
			if logs[0].Level != tc.wantLevel {
				t.Errorf("Unexpected level %v, expected %v", logs[0].Level, tc.wantLevel)
			}
			if diff := gocmp.Diff(tc.wantContext, logs[0].ContextMap()); diff != "" {
				t.Errorf("Unexpected fields (-want +got):\n%s", diff)
			}
			keys := make(map[string]bool, len(logs[0].Context))
			for _, field := range logs[0].Context {
				if keys[field.Key] {
					t.Errorf("Field %q is logged more than once", field.Key)
				}
				keys[field.Key] = true
			}
		})
	}
}

func TestCustomLogProcessorAllocationsWithoutErrorValues(t *testing.T) {
	processor := NewCustomLogProcessor(zapcore.NewNopCore())

	testCases := map[string]struct {
		level  zapcore.Level
		fields []zap.Field
	}{
		"info record": {
			level:  infoLevel,
			fields: []zap.Field{zap.String("job", "a")},
		},
		"error record": {
			level:  zapcore.ErrorLevel,
			fields: []zap.Field{zap.Error(errors.New("lookup failed"))},
		},
		"error record with a wrapped error": {
			level:  zapcore.ErrorLevel,
			fields: []zap.Field{zap.Error(fmt.Errorf("reconciling: %w", errors.New("lookup failed")))},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			entry := zapcore.Entry{Level: tc.level, Message: "Reconciler error"}
			allocs := testing.AllocsPerRun(100, func() {
				_ = processor.Write(entry, tc.fields)
			})
			if allocs != 0 {
				t.Errorf("Write allocated %v times, expected 0", allocs)
			}
		})
	}
}

func TestCustomLogProcessorKeepsCallerFields(t *testing.T) {
	core, _ := observer.New(infoLevel)
	processor := NewCustomLogProcessor(core)

	// The caller's slice has spare capacity, which an append would write into.
	fields := make([]zap.Field, 0, 4)
	fields = append(fields, zap.Error(WithErrorValues(errors.New("lookup failed"), "job", "a")))
	spare := fields[:2]
	spare[1] = zap.String("owner", "caller")

	if err := processor.Write(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "Reconciler error"}, fields); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if diff := gocmp.Diff(zap.String("owner", "caller"), spare[1]); diff != "" {
		t.Errorf("Write changed the caller's fields (-want +got):\n%s", diff)
	}
}

func BenchmarkCustomLogProcessor(b *testing.B) {
	errLookup := errors.New("lookup failed")
	errWithValues := fmt.Errorf("reconciling: %w", WithErrorValues(errLookup, "ancestorJob", klog.KRef("ns", "parent")))
	workload := klog.KRef("ns", "wl")

	benchmarks := []struct {
		name    string
		logCall func(log logr.Logger)
	}{
		{
			name: "info without error",
			logCall: func(log logr.Logger) {
				log.V(2).Info("Admitted Workload", "workload", workload)
			},
		},
		{
			name: "error without values",
			logCall: func(log logr.Logger) {
				log.Error(errLookup, "Reconciler error")
			},
		},
		{
			name: "error with values",
			logCall: func(log logr.Logger) {
				log.Error(errWithValues, "Reconciler error")
			},
		},
		{
			name: "logger with values",
			logCall: func(log logr.Logger) {
				log.WithValues("workload", workload)
			},
		},
	}
	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
			core := zapcore.NewCore(encoder, zapcore.AddSync(io.Discard), infoLevel)
			log := zapr.NewLogger(zap.New(NewCustomLogProcessor(core))).WithValues("namespace", "ns", "name", "job")
			b.ReportAllocs()
			for b.Loop() {
				bm.logCall(log)
			}
		})
	}
}
