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

package behavioral

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/reporters"
	"github.com/onsi/gomega"
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	utillogging "sigs.k8s.io/kueue/pkg/util/logging"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func RunSuite(t *testing.T, suiteName string) {
	ginkgo.ReportAfterSuite("Generate JUnit Report", ConfigureSuiteReporting)
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, suiteName)
}

func ConfigureSuiteReporting(report ginkgo.Report) {
	junitConfig := reporters.JunitReportConfig{
		OmitFailureMessageAttr: true,
	}
	suiteName := uniqueSuiteName(report.SuitePath)
	reportDir := cmp.Or(os.Getenv("ARTIFACTS"), ArtifactsDir)
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "cannot create suite report directory %q", reportDir)
	}
	reportPath := filepath.Join(reportDir, "junit-"+suiteName+".xml")
	ginkgo.GinkgoLogr.Info(
		"Generating JUnit report",
		"path", reportPath,
	)
	gomega.Expect(reporters.GenerateJUnitReportWithConfig(report, reportPath, junitConfig)).To(gomega.Succeed())
}

func uniqueSuiteName(suitePath string) string {
	normalized := filepath.ToSlash(suitePath)
	if idx := strings.Index(normalized, "test/"); idx != -1 {
		normalized = normalized[idx:]
	}
	return strings.ReplaceAll(normalized, "/", "-")
}

var SetupLogger = sync.OnceFunc(func() {
	ctrl.SetLogger(NewTestingLogger(ginkgo.GinkgoWriter))
})

func NewTestingLogger(writer io.Writer) logr.Logger {
	logger, _ := NewTestingLoggerAndObservedLogs(writer)
	return logger
}

func NewTestingLoggerAndObservedLogs(writer io.Writer) (logr.Logger, *observer.ObservedLogs) {
	level := utiltesting.LogLevelWithDefault(utiltesting.DefaultLogLevel)
	zapcoreLevel := zapcore.Level(level)

	logsObserver, observedLogs := observer.New(zapcoreLevel)

	logsObserverWrapper := zaplog.WrapCore(func(core zapcore.Core) zapcore.Core {
		return utillogging.NewCustomLogProcessor(zapcore.NewTee(logsObserver, core))
	})

	opts := func(o *zap.Options) {
		o.TimeEncoder = zapcore.RFC3339NanoTimeEncoder
		o.ZapOpts = []zaplog.Option{zaplog.AddCaller(),
			logsObserverWrapper}
	}

	return zap.New(
		zap.WriteTo(writer),
		zap.UseDevMode(true),
		zap.Level(zapcoreLevel),
		opts), observedLogs
}

// getProjectBaseDir retrieves the project base directory either from an environment variable or by searching for a Makefile.
// The fallback to the search is useful for running in IDEs like vs-code which don't set the PROJECT_DIR env. variable by default.
func getProjectBaseDir() string {
	projectBasePath, found := os.LookupEnv("PROJECT_DIR")
	if found {
		return filepath.Dir(projectBasePath)
	}

	projectBaseDir, err := findMakefileDir()
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("Failed to find project base directory: %v", err))
	}
	return projectBaseDir
}

// findMakefileDir traverses directories upward from the current directory until it finds a directory containing a Makefile.
func findMakefileDir() (string, error) {
	startDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get current working directory: %w", err)
	}

	for {
		makefilePath := filepath.Join(startDir, "Makefile")
		if _, err := os.Stat(makefilePath); err == nil {
			return startDir, nil
		}

		parentDir := filepath.Dir(startDir)
		if parentDir == startDir {
			return "", errors.New("not able to locate Makefile")
		}
		startDir = parentDir
	}
}
