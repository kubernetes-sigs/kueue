/*
Copyright 2024 The Kubernetes Authors.

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

package kjobctl

import (
	"fmt"
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/list"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/test/util"
)

var _ = ginkgo.Describe("Kjobctl List", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		os.Unsetenv(list.KjobctlListRequestLimitEnvName)
	})

	ginkgo.When("List Jobs", func() {
		var (
			j1 *batchv1.Job
			j2 *batchv1.Job
			j3 *batchv1.Job
		)

		ginkgo.JustBeforeEach(func() {
			j1 = wrappers.MakeJob("j1", ns.Name).
				Profile("profile1").
				Completions(3).
				WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
				RestartPolicy(corev1.RestartPolicyOnFailure).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, j1)).To(gomega.Succeed())

			j2 = wrappers.MakeJob("j2", ns.Name).
				Profile("profile1").
				Completions(3).
				WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
				RestartPolicy(corev1.RestartPolicyOnFailure).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, j2)).To(gomega.Succeed())

			j3 = wrappers.MakeJob("very-long-job-name", ns.Name).
				Profile("profile1").
				Completions(3).
				WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
				RestartPolicy(corev1.RestartPolicyOnFailure).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, j3)).To(gomega.Succeed())
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print jobs list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kjobctl := cmd.NewKjobctlCmd(cmd.KjobctlOptions{ConfigFlags: configFlags, IOStreams: streams,
				Clock: testingclock.NewFakeClock(executeTime)})

			os.Setenv(list.KjobctlListRequestLimitEnvName, "1")
			kjobctl.SetArgs([]string{"list", "job", "--namespace", ns.Name})
			err := kjobctl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                 PROFILE    COMPLETIONS   DURATION   AGE
j1                   profile1   0/3                      %s
j2                   profile1   0/3                      %s
very-long-job-name   profile1   0/3                      %s
`,
				duration.HumanDuration(executeTime.Sub(j1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(j2.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(j3.CreationTimestamp.Time)),
			)))
		})
	})
})
