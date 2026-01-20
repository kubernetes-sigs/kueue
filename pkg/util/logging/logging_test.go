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
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestErrorLogLevelOverridenZapCoreChangesErrorLevelOfReconcilerError(t *testing.T) {
	core, observedLogs := observer.New(zapcore.WarnLevel)
	logger := zap.New(NewErrorLogLevelOverridenCore(core))

	logger.Error("Reconciler error")

	logs := observedLogs.TakeAll()

	if len(logs) != 1 {
		t.Errorf("Unexpected number of log entries %v, expected 1\n", len(logs))
	}

	log := logs[0]
	if log.Message != "Reconciler error" || logs[0].Level != zapcore.WarnLevel {
		t.Errorf("Unexpected log entry %v\n", log)
	}
}

func TestErrorLogLevelOverridenZapCoreLeavesRestOfLogsIntact(t *testing.T) {
	core, observedLogs := observer.New(zapcore.InfoLevel)
	logger := zap.New(NewErrorLogLevelOverridenCore(core))

	logger.Info("Reconciler error")
	logger.Error("Some other error")
	fieldKey := "some field key"
	fieldValue := "some field value"
	someField := zap.String(fieldKey, fieldValue)
	logger.Warn("Some log with fields", someField)

	logs := observedLogs.TakeAll()

	if len(logs) != 3 {
		t.Errorf("Unexpected number of log entries %v, expected 3\n", len(logs))
	}

	expectedLogs := []struct {
		Message string
		Level   zapcore.Level
		Fields  []zap.Field
	}{{"Reconciler error", zapcore.InfoLevel, nil},
		{"Some other error", zapcore.ErrorLevel, nil},
		{"Some log with fields", zapcore.WarnLevel, []zap.Field{someField}},
	}

	for i, expected := range expectedLogs {
		log := logs[i]
		if log.Message != expected.Message || log.Level != expected.Level {
			t.Errorf("Unexpected log entry %v\n", log)
		}
	}

	logWithFieldContext := logs[2].ContextMap()

	if retrievedVal := logWithFieldContext[fieldKey]; retrievedVal != fieldValue {
		t.Errorf("Unexpected field value %v, expected %v\n", retrievedVal, fieldValue)
	}

}
