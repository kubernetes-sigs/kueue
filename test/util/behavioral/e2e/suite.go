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

package e2e

import (
	"cmp"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/reporters"
	"github.com/onsi/gomega"
)

// ArtifactsDir is the directory where test artifacts are stored
var ArtifactsDir string

func init() {
	ArtifactsDir = cmp.Or(os.Getenv("ARTIFACTS"), filepath.Join(ProjectBaseDir, "artifacts"))
}

// RunE2ESuite runs an E2E Ginkgo test suite
func RunE2ESuite(t *testing.T, suiteName string) {
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = suiteName + ": " + ver
	}
	RunSuite(t, suiteName)
}

// RunSuite runs a Ginkgo test suite
func RunSuite(t *testing.T, suiteName string) {
	ginkgo.ReportAfterSuite("Generate JUnit Report", ConfigureSuiteReporting)
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, suiteName)
}

// ConfigureSuiteReporting generates a JUnit report for the test suite
func ConfigureSuiteReporting(report ginkgo.Report) {
	junitConfig := reporters.JunitReportConfig{
		OmitFailureMessageAttr: true,
	}
	suiteName := uniqueSuiteName(report.SuitePath)
	reportDir := ArtifactsDir
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
