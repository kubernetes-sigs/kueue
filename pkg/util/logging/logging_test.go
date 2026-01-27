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
	"reflect"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const reconcilerErrorMessage = "Reconciler error"

const reconcilerErrorDetails = "Some prefix: the object has been modified; please apply your changes to the latest version and try again"

func TestCustomLogProcessorChangesErrorLevelOfConcurrentModificationReconcilerError(t *testing.T) {
	core, observedLogs := observer.New(zapcore.WarnLevel)
	logger := zap.New(NewCustomLogProcessor(core))

	errorDetailsField := newErrorField(reconcilerErrorDetails)

	logger.Error(reconcilerErrorMessage, errorDetailsField)

	logs := observedLogs.TakeAll()

	assertThereIsOnlyOneReconcilerErrorWithWarningLevel(t, logs)

	expectedFields := []zap.Field{errorDetailsField}
	assertLoggedEntryContainsCorrectFields(t, logs[0], expectedFields)

	someOtherField := zap.String("Some field", "Some value")
	childLogger := logger.With(someOtherField)
	childLogger.Error(reconcilerErrorMessage, errorDetailsField)

	logs = observedLogs.TakeAll()

	assertThereIsOnlyOneReconcilerErrorWithWarningLevel(t, logs)
	expectedFields = []zap.Field{someOtherField, errorDetailsField}
	assertLoggedEntryContainsCorrectFields(t, logs[0], expectedFields)

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

func assertThereIsOnlyOneReconcilerErrorWithWarningLevel(t *testing.T, logs []observer.LoggedEntry) {
	if len(logs) != 1 {
		t.Errorf("Unexpected number of log entries %v, expected 1\n", len(logs))
	}

	log := logs[0]
	if log.Message != reconcilerErrorMessage || logs[0].Level != zapcore.WarnLevel {
		t.Errorf("Unexpected log entry %v\n", log)
	}
}

func assertLoggedEntryContainsCorrectFields(t *testing.T, entry observer.LoggedEntry, expectedFields []zap.Field) {
	if !reflect.DeepEqual(entry.Context, expectedFields) {
		t.Errorf("Unexpected field values %v, expected %v\n", entry.Context, expectedFields)
	}
}

func TestCustomLogProcessorLeavesRestOfLogsIntact(t *testing.T) {
	core, observedLogs := observer.New(zapcore.InfoLevel)
	logger := zap.New(NewCustomLogProcessor(core))

	otherErrorMessage := "Some other error"
	messageOfWarningLogWithField := "Some log with fields"
	concurrentModificationErrorDetailsField := newErrorField(reconcilerErrorDetails)
	otherErrorDetailsField := newErrorField("Some other error details")

	logger.Error(otherErrorMessage)
	logger.Error(reconcilerErrorMessage) // lacking appropriate error details
	logger.Error(reconcilerErrorMessage, otherErrorDetailsField)

	logger.Info(reconcilerErrorMessage, concurrentModificationErrorDetailsField)
	fieldKey := "some field key"
	fieldValue := "some field value"
	someField := zap.String(fieldKey, fieldValue)
	logger.Warn(messageOfWarningLogWithField, someField)

	logs := observedLogs.TakeAll()

	expectedLogs := []struct {
		Message string
		Level   zapcore.Level
		Fields  []zap.Field
	}{
		{otherErrorMessage, zapcore.ErrorLevel, []zap.Field{}},
		{reconcilerErrorMessage, zapcore.ErrorLevel, []zap.Field{}},
		{reconcilerErrorMessage, zapcore.ErrorLevel, []zap.Field{otherErrorDetailsField}},
		{reconcilerErrorMessage, zapcore.InfoLevel, []zap.Field{concurrentModificationErrorDetailsField}},
		{messageOfWarningLogWithField, zapcore.WarnLevel, []zap.Field{someField}},
	}

	if len(logs) != len(expectedLogs) {
		t.Errorf("Unexpected number of log entries %v, expected %v\n", len(logs), len(expectedLogs))
	}

	for i, expected := range expectedLogs {
		log := logs[i]
		if log.Message != expected.Message || log.Level != expected.Level {
			t.Errorf("Unexpected log entry %v\n", log)
		}

		assertLoggedEntryContainsCorrectFields(t, log, expected.Fields)
	}

}
