/*
Copyright 2022 The Kubernetes Authors.

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
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	visibilityv1alpha1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1alpha1"
	"sigs.k8s.io/kueue/test/util"
	//+kubebuilder:scaffold:imports
)

var (
	k8sClient                    client.Client
	ctx                          context.Context
	visibilityClient             visibilityv1alpha1.VisibilityV1alpha1Interface
	impersonatedVisibilityClient visibilityv1alpha1.VisibilityV1alpha1Interface
)

const (
	Timeout  = time.Minute
	Interval = time.Millisecond * 250
)

func TestAPIs(t *testing.T) {
	suiteName := "End To End Suite"
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = fmt.Sprintf("%s: %s", suiteName, ver)
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t,
		suiteName,
	)
}

var _ = ginkgo.BeforeSuite(func() {
	k8sClient = util.CreateClientUsingCluster("")
	visibilityClient = util.CreateVisibilityClient("")
	impersonatedVisibilityClient = util.CreateVisibilityClient("system:serviceaccount:kueue-system:default")
	ctx = context.Background()
	util.KueueReadyForTesting(ctx, k8sClient)
})
