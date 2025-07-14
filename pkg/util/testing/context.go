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

func LogLevelWithDefault(defaultLogLevel int) int {
	level, err := strconv.Atoi(os.Getenv("TEST_LOG_LEVEL"))
	if err != nil {
		return defaultLogLevel
	}
	return level
}

func ContextWithLog(t *testing.T) (context.Context, logr.Logger) {
	logger := testr.NewWithOptions(t, testr.Options{
		Verbosity: LogLevelWithDefault(2),
	})
	return ctrl.LoggerInto(t.Context(), logger), logger
}
