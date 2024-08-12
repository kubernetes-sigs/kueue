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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
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
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                 PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1                   profile1                 0/3                      %s
j2                   profile1                 0/3                      %s
very-long-job-name   profile1                 0/3                      %s
`,
				duration.HumanDuration(executeTime.Sub(j1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(j2.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(j3.CreationTimestamp.Time)),
			)))
		})
	})

	ginkgo.When("List RayJobs", func() {
		var (
			rj1 *rayv1.RayJob
			rj2 *rayv1.RayJob
			rj3 *rayv1.RayJob
		)

		ginkgo.JustBeforeEach(func() {
			rj1 = wrappers.MakeRayJob("rj1", ns.Name).
				Profile("profile1").
				Suspend(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rj1)).To(gomega.Succeed())

			rj2 = wrappers.MakeRayJob("rj2", ns.Name).
				Profile("profile1").
				Suspend(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rj2)).To(gomega.Succeed())

			rj3 = wrappers.MakeRayJob("very-long-job-name", ns.Name).
				Profile("profile1").
				Suspend(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rj3)).To(gomega.Succeed())
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print jobs list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kjobctl := cmd.NewKjobctlCmd(cmd.KjobctlOptions{ConfigFlags: configFlags, IOStreams: streams,
				Clock: testingclock.NewFakeClock(executeTime)})

			os.Setenv(list.KjobctlListRequestLimitEnvName, "1")
			kjobctl.SetArgs([]string{"list", "rayjob", "--namespace", ns.Name})
			err := kjobctl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                 PROFILE    LOCAL QUEUE   JOB STATUS   DEPLOYMENT STATUS   START TIME   END TIME   AGE
rj1                  profile1                                                                          %s
rj2                  profile1                                                                          %s
very-long-job-name   profile1                                                                          %s
`,
				duration.HumanDuration(executeTime.Sub(rj1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(rj2.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(rj3.CreationTimestamp.Time)),
			)))
		})
	})

	ginkgo.When("List RayClusters", func() {
		var (
			rc1 *rayv1.RayCluster
			rc2 *rayv1.RayCluster
			rc3 *rayv1.RayCluster
		)

		ginkgo.JustBeforeEach(func() {
			rc1 = wrappers.MakeRayCluster("rc1", ns.Name).
				Profile("profile1").
				Spec(*wrappers.MakeRayClusterSpec().
					HeadGroupSpec(rayv1.HeadGroupSpec{
						RayStartParams: make(map[string]string),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "ray-head", Image: "rayproject/ray:2.9.0"},
								},
							},
						},
					}).
					WithWorkerGroupSpec(rayv1.WorkerGroupSpec{
						RayStartParams: make(map[string]string),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "ray-worker", Image: "rayproject/ray:2.9.0"},
								},
							},
						},
					}).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rc1)).To(gomega.Succeed())

			rc2 = wrappers.MakeRayCluster("rc2", ns.Name).
				Profile("profile1").
				Spec(*wrappers.MakeRayClusterSpec().
					HeadGroupSpec(rayv1.HeadGroupSpec{
						RayStartParams: make(map[string]string),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "ray-head", Image: "rayproject/ray:2.9.0"},
								},
							},
						},
					}).
					WithWorkerGroupSpec(rayv1.WorkerGroupSpec{
						RayStartParams: make(map[string]string),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "ray-worker", Image: "rayproject/ray:2.9.0"},
								},
							},
						},
					}).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rc2)).To(gomega.Succeed())

			rc3 = wrappers.MakeRayCluster("very-long-ray-cluster-name", ns.Name).
				Profile("profile1").
				Spec(*wrappers.MakeRayClusterSpec().
					HeadGroupSpec(rayv1.HeadGroupSpec{
						RayStartParams: make(map[string]string),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "ray-head", Image: "rayproject/ray:2.9.0"},
								},
							},
						},
					}).
					WithWorkerGroupSpec(rayv1.WorkerGroupSpec{
						RayStartParams: make(map[string]string),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "ray-worker", Image: "rayproject/ray:2.9.0"},
								},
							},
						},
					}).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rc3)).To(gomega.Succeed())
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print ray clusters list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kjobctl := cmd.NewKjobctlCmd(cmd.KjobctlOptions{ConfigFlags: configFlags, IOStreams: streams,
				Clock: testingclock.NewFakeClock(executeTime)})

			os.Setenv(list.KjobctlListRequestLimitEnvName, "1")
			kjobctl.SetArgs([]string{"list", "raycluster", "--namespace", ns.Name})
			err := kjobctl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                         PROFILE    LOCAL QUEUE   DESIRED WORKERS   AVAILABLE WORKERS   CPUS   MEMORY   GPUS   STATUS   AGE
rc1                          profile1                 0                 0                   0      0        0               %s
rc2                          profile1                 0                 0                   0      0        0               %s
very-long-ray-cluster-name   profile1                 0                 0                   0      0        0               %s
`,
				duration.HumanDuration(executeTime.Sub(rc1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(rc2.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(rc3.CreationTimestamp.Time)),
			)))
		})
	})
})
