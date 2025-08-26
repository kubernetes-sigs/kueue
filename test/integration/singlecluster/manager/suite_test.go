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

package manager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/integration/framework"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Manager SetupIndexes Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		goPath := os.Getenv("GOPATH")
		if goPath == "" {
			if cmd, err := exec.LookPath("go"); err == nil {
				if out, err := exec.Command(cmd, "env", "GOPATH").Output(); err == nil {
					goPath = strings.TrimSpace(string(out))
				}
			}
		}
		if goPath != "" {
			setupEnvtest := filepath.Join(goPath, "bin", "setup-envtest")
			if out, err := exec.Command(setupEnvtest, "use", "-p", "path").Output(); err == nil {
				envtestPath := strings.TrimSpace(string(out))
				if envtestPath != "" {
					_ = os.Setenv("KUBEBUILDER_ASSETS", envtestPath)
				}
			}
		}
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			if homeDir, err := os.UserHomeDir(); err == nil {
				fallbackPath := filepath.Join(homeDir, ".local", "share", "kubebuilder-envtest", "k8s", "1.33.0-linux-amd64")
				if _, err := os.Stat(fallbackPath); err == nil {
					_ = os.Setenv("KUBEBUILDER_ASSETS", fallbackPath)
				}
			}
		}
	}

	fwk = &framework.Framework{}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})
