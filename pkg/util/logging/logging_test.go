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
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	gocmp "github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	crzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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

type panickingMarshaler struct{}

func (panickingMarshaler) MarshalLog() any {
	panic("marshal failed")
}

func TestCustomLogProcessorErrorValues(t *testing.T) {
	const writeConflictMessage = "Operation cannot be fulfilled on workloads.kueue.x-k8s.io \"wl\": " +
		"the object has been modified; please apply your changes to the latest version and try again"

	errLookup := errors.New("lookup failed")
	errOther := errors.New("other failure")
	parentJob := klog.KRef("ns", "parent")
	workload := klog.KRef("ns", "wl")
	otherWorkload := klog.KRef("ns", "wl-other")
	thirdWorkload := klog.KRef("ns", "wl-third")

	exactValues := make([]any, 0, 2*maxErrorValues)
	wantExactValues := map[string]any{
		"error": errLookup.Error(),
	}
	for i := range maxErrorValues {
		key := fmt.Sprintf("key%02d", i)
		exactValues = append(exactValues, key, "value")
		wantExactValues[key] = "value"
	}

	manyValues := append(slices.Clone(exactValues), fmt.Sprintf("key%02d", maxErrorValues), "value")
	wantManyValues := map[string]any{
		"error":                  errLookup.Error(),
		errorValuesIncompleteKey: true,
	}
	for i := range maxErrorValues {
		wantManyValues[fmt.Sprintf("key%02d", i)] = "value"
	}

	// More than maxErrorValues pairs on one call, even though unique pairs fit.
	paddedValues := make([]any, 0, 2*(maxErrorValues+1))
	for range maxErrorValues {
		paddedValues = append(paddedValues, "job", "a")
	}
	paddedValues = append(paddedValues, "late", "dropped")

	// Joined errors together exceed maxErrorValues across separate merge calls.
	halfValues := make([]any, 0, maxErrorValues)
	for i := range maxErrorValues / 2 {
		halfValues = append(halfValues, fmt.Sprintf("k%02d", i), "v")
	}
	wantSplitValues := map[string]any{
		"error":                  errLookup.Error() + "\n" + errOther.Error() + "\n" + errOther.Error(),
		errorValuesIncompleteKey: true,
	}
	otherHalfValues := make([]any, 0, maxErrorValues+2)
	for i := range maxErrorValues/2 + 1 {
		key := fmt.Sprintf("m%02d", i)
		otherHalfValues = append(otherHalfValues, key, "v")
		if i < maxErrorValues/2 {
			wantSplitValues[key] = "v"
		}
	}
	for i := range maxErrorValues / 2 {
		wantSplitValues[fmt.Sprintf("k%02d", i)] = "v"
	}

	// Every error in exactNodeErr fits within maxErrorNodes (outer + join +
	// maxErrorNodes-4 leaf errors + valued error + its inner nil-unwrapping error = maxErrorNodes).
	// Nil unwrap results must not consume node budget or mark the record incomplete.
	nilUnwrapErr := &url.Error{Op: "Get", URL: "https://example.com"}
	exactNodeErrs := make([]error, 0, maxErrorNodes-3)
	exactNodeErrs = append(exactNodeErrs, nilUnwrapErr)
	for range maxErrorNodes - 5 {
		exactNodeErrs = append(exactNodeErrs, errLookup)
	}
	exactNodeErr := errors.Join(append(exactNodeErrs, ErrorWithValues(nilUnwrapErr, "queue", "q"))...)

	// The last error is the first one that the search does not visit.
	wideErrs := make([]error, 0, maxErrorNodes-1)
	for range maxErrorNodes - 2 {
		wideErrs = append(wideErrs, errLookup)
	}
	wideErr := errors.Join(append(wideErrs, ErrorWithValues(errOther, "queue", "q"))...)

	// A single-unwrap chain longer than maxErrorNodes stops in collect.
	deepErr := ErrorWithValues(errLookup, "queue", "q")
	for range maxErrorNodes {
		deepErr = fmt.Errorf("wrapped: %w", deepErr)
	}

	// Error returns "<nil>" for a nil *net.OpError, but Unwrap panics.
	var nilOpErr *net.OpError

	// Comparable struct type holding an uncomparable dynamic interface value.
	type anyBox struct {
		V any
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
				err := ErrorWithValues(errLookup, "ancestorJob", parentJob, "reason", "timeout")
				log.Error(fmt.Errorf("reconciling: %w", err), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":       "reconciling: lookup failed",
				"ancestorJob": parentJob.MarshalLog(),
				"reason":      "timeout",
			},
		},
		"caller slice mutation does not change logged values": {
			logCall: func(log logr.Logger) {
				keysAndValues := []any{"job", "a"}
				err := ErrorWithValues(errLookup, keysAndValues...)
				keysAndValues[1] = "changed by caller"
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "lookup failed",
				"job":   "a",
			},
		},
		"values of an error wrapping a nil inner error are logged": {
			logCall: func(log logr.Logger) {
				err := ErrorWithValues(&url.Error{Op: "Get", URL: "https://example.com"}, "ancestorJob", parentJob)
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":       (&url.Error{Op: "Get", URL: "https://example.com"}).Error(),
				"ancestorJob": parentJob.MarshalLog(),
			},
		},
		"values of every error in a chain are logged": {
			logCall: func(log logr.Logger) {
				inner := ErrorWithValues(errLookup, "workload", workload)
				outer := ErrorWithValues(fmt.Errorf("suspending: %w", inner), "ancestorJob", parentJob)
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
					ErrorWithValues(errLookup, "clusterQueue", "cq"),
					fmt.Errorf("updating: %w", ErrorWithValues(errOther, "resourceFlavor", "rf")),
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
					ErrorWithValues(errLookup, "clusterQueue", "cq"),
					ErrorWithValues(errOther, "resourceFlavor", "rf"),
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
				log.V(3).Info("Ignored the error", "error", ErrorWithValues(errLookup, "ancestorJob", parentJob))
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"error":       "lookup failed",
				"ancestorJob": parentJob.MarshalLog(),
			},
		},
		"values of an error logged under another key are logged": {
			logCall: func(log logr.Logger) {
				log.V(3).Info("Ignored the error", "cause", ErrorWithValues(errLookup, "ancestorJob", parentJob))
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"cause":       "lookup failed",
				"ancestorJob": parentJob.MarshalLog(),
			},
		},
		"values of every error field are logged": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errLookup, "job", "a"), "Reconciler error",
					"cause", ErrorWithValues(errOther, "queue", "q"))
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "lookup failed",
				"cause": "other failure",
				"job":   "a",
				"queue": "q",
			},
		},
		"concurrent modification error keeps its values": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errors.New(writeConflictMessage), "ancestorJob", parentJob), "Reconciler error")
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"error":       writeConflictMessage,
				"ancestorJob": parentJob.MarshalLog(),
			},
		},
		"concurrent modification error under another key is not relabeled": {
			logCall: func(log logr.Logger) {
				log.Error(errLookup, "Reconciler error", "cause", errors.New(writeConflictMessage))
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "lookup failed",
				"cause": writeConflictMessage,
			},
		},
		"key of the log call is kept": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errLookup, "job", "from-error"), "Reconciler error", "job", "from-call")
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
				log.Error(ErrorWithValues(errLookup, "namespace", "other-ns", "name", "other"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"namespace":     "ns",
				"name":          "child",
				"error":         "lookup failed",
				errorContextKey: map[string]any{"namespace": "other-ns", "name": "other"},
			},
		},
		"error key of the record is kept": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errLookup, "error", "from-error"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":         "lookup failed",
				errorContextKey: map[string]any{"error": "from-error"},
			},
		},
		"outer error keeps a key that an inner error also uses": {
			logCall: func(log logr.Logger) {
				inner := ErrorWithValues(errLookup, "workload", otherWorkload)
				outer := ErrorWithValues(fmt.Errorf("suspending: %w", inner), "workload", workload)
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
				inner := ErrorWithValues(errLookup, "workload", workload, "queue", "q")
				outer := ErrorWithValues(fmt.Errorf("suspending: %w", inner), "workload", workload)
				log.Error(outer, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":    "suspending: lookup failed",
				"workload": workload.MarshalLog(),
				"queue":    "q",
			},
		},
		"equal nil values of one key in an error chain are logged once": {
			logCall: func(log logr.Logger) {
				inner := ErrorWithValues(errLookup, "owner", nil)
				outer := ErrorWithValues(fmt.Errorf("suspending: %w", inner), "owner", nil)
				log.Error(outer, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": "suspending: lookup failed",
				"owner": nil,
			},
		},
		"nil and non-nil values of one key are not deduplicated": {
			logCall: func(log logr.Logger) {
				inner := ErrorWithValues(errLookup, "owner", "parent")
				outer := ErrorWithValues(fmt.Errorf("suspending: %w", inner), "owner", nil)
				log.Error(outer, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":         "suspending: lookup failed",
				"owner":         nil,
				errorContextKey: map[string]any{"owner": "parent"},
			},
		},
		"equal key and value on logger and error are logged twice": {
			logCall: func(log logr.Logger) {
				err := ErrorWithValues(errLookup, "workload", workload)
				log.WithValues("workload", workload).Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":         "lookup failed",
				"workload":      workload.MarshalLog(),
				errorContextKey: map[string]any{"workload": workload.MarshalLog()},
			},
		},
		"values that cannot be compared are logged": {
			logCall: func(log logr.Logger) {
				inner := ErrorWithValues(errLookup, "flavors", []string{"rf"}, "box", anyBox{V: []string{"rf"}})
				outer := ErrorWithValues(fmt.Errorf("suspending: %w", inner), "flavors", []string{"rf"}, "box", anyBox{V: []string{"rf"}}, "queues", []string{"q"})
				log.Error(outer, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":   "suspending: lookup failed",
				"flavors": []any{"rf"},
				"box":     anyBox{V: []string{"rf"}},
				"queues":  []any{"q"},
				errorContextKey: map[string]any{
					"flavors": []any{"rf"},
					"box":     anyBox{V: []string{"rf"}},
				},
			},
		},
		"third value of a key is dropped and reported": {
			logCall: func(log logr.Logger) {
				err := errors.Join(
					ErrorWithValues(errLookup, "workload", workload),
					ErrorWithValues(errOther, "workload", otherWorkload),
					ErrorWithValues(errOther, "workload", thirdWorkload),
				)
				log.Error(err, "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  "lookup failed\nother failure\nother failure",
				"workload":               workload.MarshalLog(),
				errorContextKey:          map[string]any{"workload": otherWorkload.MarshalLog()},
				errorValuesIncompleteKey: true,
			},
		},
		"API objects in the error context are logged by reference": {
			logCall: func(log logr.Logger) {
				// Pod implements fmt.Stringer (StringerType); Workload does not (ReflectType).
				pod := testingpod.MakePod("pod", "ns").Obj()
				wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
				log.Error(ErrorWithValues(errLookup, "pod", pod, "workload", wl),
					"Reconciler error", "pod", "from-call", "workload", "from-call")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"pod":      "from-call",
				"workload": "from-call",
				"error":    "lookup failed",
				errorContextKey: map[string]any{
					"pod":      klog.KRef("ns", "pod").MarshalLog(),
					"workload": klog.KRef("ns", "wl").MarshalLog(),
				},
			},
		},
		"panicking logr.Marshaler value is recovered": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errLookup, "obj", panickingMarshaler{}), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":    "lookup failed",
				"objError": "PANIC=marshal failed",
			},
		},
		"key without a value is ignored and reported": {
			logCall: func(log logr.Logger) {
				// Passed as a slice, because loggercheck rejects such a call.
				inner := ErrorWithValues(errLookup, "workload", workload)
				keysAndValues := []any{"job", "a", "queue"}
				log.Error(ErrorWithValues(fmt.Errorf("suspending: %w", inner), keysAndValues...), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  "suspending: lookup failed",
				"job":                    "a",
				"workload":               workload.MarshalLog(),
				errorValuesIncompleteKey: true,
			},
		},
		"values from the first key that is not a string are ignored and reported": {
			logCall: func(log logr.Logger) {
				keysAndValues := []any{"job", "a", 1, "b", "queue", "c"}
				log.Error(ErrorWithValues(errLookup, keysAndValues...), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  "lookup failed",
				"job":                    "a",
				errorValuesIncompleteKey: true,
			},
		},
		"values at the record limit are logged completely": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errLookup, exactValues...), "Reconciler error")
			},
			wantLevel:   zapcore.ErrorLevel,
			wantContext: wantExactValues,
		},
		"values beyond the limit on one error are dropped and reported": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errLookup, manyValues...), "Reconciler error")
			},
			wantLevel:   zapcore.ErrorLevel,
			wantContext: wantManyValues,
		},
		"duplicate-padded values beyond one-call limit are capped and reported": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(errLookup, paddedValues...), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  "lookup failed",
				"job":                    "a",
				errorValuesIncompleteKey: true,
			},
		},
		"values beyond the limit across errors are dropped and reported": {
			logCall: func(log logr.Logger) {
				err := errors.Join(
					ErrorWithValues(errLookup, halfValues...),
					ErrorWithValues(errOther, otherHalfValues...),
					ErrorWithValues(errOther, "extra", "dropped"),
				)
				log.Error(err, "Reconciler error")
			},
			wantLevel:   zapcore.ErrorLevel,
			wantContext: wantSplitValues,
		},
		"values of other errors are kept when reading one error panics": {
			logCall: func(log logr.Logger) {
				err := errors.Join(
					nilOpErr,
					ErrorWithValues(errLookup, "clusterQueue", "cq"),
				)
				log.V(3).Info("Ignored the error", "cause", err)
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"cause":                  "<nil>\nlookup failed",
				"clusterQueue":           "cq",
				errorValuesIncompleteKey: true,
			},
		},
		"errors at the node limit are read completely": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(exactNodeErr, "job", "a"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error": exactNodeErr.Error(),
				"job":   "a",
				"queue": "q",
			},
		},
		"errors beyond the node limit are not read": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(wideErr, "job", "a"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  wideErr.Error(),
				"job":                    "a",
				errorValuesIncompleteKey: true,
			},
		},
		"deep error chain beyond the node limit is not read": {
			logCall: func(log logr.Logger) {
				log.Error(ErrorWithValues(deepErr, "job", "a"), "Reconciler error")
			},
			wantLevel: zapcore.ErrorLevel,
			wantContext: map[string]any{
				"error":                  deepErr.Error(),
				"job":                    "a",
				errorValuesIncompleteKey: true,
			},
		},
		"concurrent modification error is relabeled when reading its values panics": {
			logCall: func(log logr.Logger) {
				log.Error(fmt.Errorf("%s: %w", writeConflictMessage, nilOpErr), "Reconciler error")
			},
			wantLevel: warningLevel,
			wantContext: map[string]any{
				"error": writeConflictMessage + ": <nil>",
			},
		},
		"error holding a nil pointer does not panic": {
			logCall: func(log logr.Logger) {
				var err *url.Error
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
	fields = append(fields, zap.Error(ErrorWithValues(errors.New("lookup failed"), "job", "a")))
	spare := fields[:2]
	spare[1] = zap.String("owner", "caller")

	if err := processor.Write(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "Reconciler error"}, fields); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if diff := gocmp.Diff(zap.String("owner", "caller"), spare[1]); diff != "" {
		t.Errorf("Write changed the caller's fields (-want +got):\n%s", diff)
	}
}

func TestCustomLogProcessorJSONSerialization(t *testing.T) {
	var buf bytes.Buffer
	log := crzap.New(
		crzap.WriteTo(&buf),
		crzap.UseDevMode(false),
		crzap.Level(infoLevel),
		crzap.RawZapOpts(zap.WrapCore(NewCustomLogProcessor)),
	).WithValues("name", "child")

	pod := testingpod.MakePod("pod", "ns").Obj()
	inner := ErrorWithValues(errors.New("lookup failed"), "pod", pod, "name", "other", "name", "dropped")
	outer := ErrorWithValues(fmt.Errorf("suspending: %w", inner), "ancestorJob", klog.KRef("ns", "parent"))
	log.Error(outer, "Reconciler error", "pod", "from-call")

	trimmed := strings.TrimSpace(buf.String())
	if trimmed == "" {
		t.Fatal("Got 0 JSON log lines, want 1")
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 1 {
		t.Fatalf("Got %d JSON log lines, want 1:\n%s", len(lines), buf.String())
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("Failed to parse JSON log record %q: %v", lines[0], err)
	}
	delete(got, "ts")
	delete(got, "stacktrace")

	want := map[string]any{
		"level":                      "error",
		"msg":                        "Reconciler error",
		"name":                       "child",
		"pod":                        "from-call",
		"error":                      "suspending: lookup failed",
		"ancestorJob":                map[string]any{"name": "parent", "namespace": "ns"},
		"kueueErrorValuesIncomplete": true,
		"kueueErrorContext": map[string]any{
			"name": "other",
			"pod":  map[string]any{"name": "pod", "namespace": "ns"},
		},
	}
	if diff := gocmp.Diff(want, got); diff != "" {
		t.Errorf("Unexpected JSON log record (-want +got):\n%s", diff)
	}
}

func BenchmarkCustomLogProcessor(b *testing.B) {
	errLookup := errors.New("lookup failed")
	errWithValues := fmt.Errorf("reconciling: %w", ErrorWithValues(errLookup, "ancestorJob", klog.KRef("ns", "parent")))
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
