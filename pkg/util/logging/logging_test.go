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
	"fmt"
	"reflect"
	"slices"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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
	concurrentModificationErrorDetailsFieldWithCustomPrefix := newErrorField(fmt.Sprintf("clearing admission: %v", concurrentModificationErrorDetails))

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
		"Changes error level of concurrent modification log with custom prefix": {
			logCall: func() {
				logger.Error(someOtherErrorMessage, concurrentModificationErrorDetailsFieldWithCustomPrefix)
			},
			expectedLog: observedLog{someOtherErrorMessage, warningLevel, []zap.Field{concurrentModificationErrorDetailsFieldWithCustomPrefix}},
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
