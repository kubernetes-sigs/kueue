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

package testing

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	ctrl "sigs.k8s.io/controller-runtime"
)

const DefaultLogLevel = -3

func LogLevelWithDefault(defaultLogLevel int) int {
	level, err := strconv.Atoi(os.Getenv("TEST_LOG_LEVEL"))
	if err != nil {
		return defaultLogLevel
	}
	return level
}

func NewLogger(t *testing.T) logr.Logger {
	// Map TEST_LOG_LEVEL to testr verbosity so that lower (more negative)
	// values increase verbosity consistently with integration/e2e logging.
	level := LogLevelWithDefault(DefaultLogLevel)
	// testr expects higher Verbosity for more logs. Our convention is
	// more negative TEST_LOG_LEVEL means more verbose. Translate by negating
	// the level, so -3 => 3. Positive levels result in negative verbosity
	// which effectively disables extra V logs.
	return testr.NewWithOptions(t, testr.Options{
		Verbosity: -level,
	})
}

func ContextWithLog(t *testing.T) (context.Context, logr.Logger) {
	logger := NewLogger(t)
	return ctrl.LoggerInto(t.Context(), logger), logger
}
