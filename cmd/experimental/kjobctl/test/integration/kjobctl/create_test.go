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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/test/util"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

var _ = ginkgo.Describe("Kjobctl Create", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Create a Job", func() {
		var (
			jobTemplate *v1alpha1.JobTemplate
			profile     *v1alpha1.ApplicationProfile
		)

		ginkgo.BeforeEach(func() {
			jobTemplate = wrappers.MakeJobTemplate("job-template", ns.Name).
				RestartPolicy(corev1.RestartPolicyOnFailure).
				WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
				WithContainer(*wrappers.MakeContainer("c2", "sleep").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, jobTemplate)).To(gomega.Succeed())

			profile = wrappers.MakeApplicationProfile("profile", ns.Name).
				WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, profile)).To(gomega.Succeed())
		})

		ginkgo.It("Should create job", func() {
			testStartTime := time.Now()

			ginkgo.By("Create a Job", func() {
				streams, _, out, outErr := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)

				kjobctlCmd := cmd.NewKjobctlCmd(cmd.KjobctlOptions{
					ConfigFlags: configFlags,
					IOStreams:   streams,
					Clock:       testingclock.NewFakeClock(testStartTime),
				})
				kjobctlCmd.SetOut(out)
				kjobctlCmd.SetErr(outErr)
				kjobctlCmd.SetArgs([]string{
					"create", "job",
					"-n", ns.Name,
					"--profile", profile.Name,
					"--cmd", "sleep 60s",
					"--parallelism", "2",
					"--completions", "3",
					"--request", "cpu=100m,memory=4Gi",
					"--localqueue", "lq1",
				})

				err := kjobctlCmd.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, out)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
				gomega.Expect(out.String()).Should(gomega.MatchRegexp(
					fmt.Sprintf("^job.batch\\/%s-[a-zA-Z0-9]+ created\\n$", profile.Name)))
			})

			ginkgo.By("Check that Job created", func() {
				timestamp := testStartTime.Format(time.RFC3339)
				expectedTaskName := fmt.Sprintf("%s_%s", ns.Name, profile.Name)
				expectedProfileName := fmt.Sprintf("%s_%s", ns.Name, profile.Name)
				expectedTaskID := fmt.Sprintf("%s_%s_%s_%s", userID, timestamp, ns.Name, profile.Name)

				jobList := &batchv1.JobList{}
				gomega.Expect(k8sClient.List(ctx, jobList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gomega.Expect(jobList.Items).To(gomega.HaveLen(1))
				gomega.Expect(jobList.Items[0].Labels[constants.ProfileLabel]).To(gomega.Equal(profile.Name))
				gomega.Expect(jobList.Items[0].Labels[kueueconstants.QueueLabel]).To(gomega.Equal("lq1"))
				gomega.Expect(jobList.Items[0].Name).To(gomega.HavePrefix(profile.Name))
				gomega.Expect(jobList.Items[0].Spec.Parallelism).To(gomega.Equal(ptr.To[int32](2)))
				gomega.Expect(jobList.Items[0].Spec.Completions).To(gomega.Equal(ptr.To[int32](3)))
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.RestartPolicy).To(gomega.Equal(corev1.RestartPolicyOnFailure))
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.Containers).To(gomega.HaveLen(2))
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.Containers[0].Command).To(gomega.Equal([]string{"sleep", "60s"}))
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.Containers[0].Resources.Requests).To(gomega.Equal(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				}))
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.Containers[0].Env).To(gomega.Equal([]corev1.EnvVar{
					{Name: constants.EnvVarNameUserID, Value: userID},
					{Name: constants.EnvVarTaskName, Value: expectedTaskName},
					{Name: constants.EnvVarTaskID, Value: expectedTaskID},
					{Name: constants.EnvVarNameProfile, Value: expectedProfileName},
					{Name: constants.EnvVarNameTimestamp, Value: timestamp},
				}))
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.Containers[1].Command).To(gomega.BeNil())
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.Containers[1].Resources.Requests).To(gomega.BeNil())
				gomega.Expect(jobList.Items[0].Spec.Template.Spec.Containers[1].Env).To(gomega.Equal([]corev1.EnvVar{
					{Name: constants.EnvVarNameUserID, Value: userID},
					{Name: constants.EnvVarTaskName, Value: expectedTaskName},
					{Name: constants.EnvVarTaskID, Value: expectedTaskID},
					{Name: constants.EnvVarNameProfile, Value: expectedProfileName},
					{Name: constants.EnvVarNameTimestamp, Value: timestamp},
				}))
			})
		})

		// SimpleDynamicClient didn't allow to check server dry run flag.
		ginkgo.It("Shouldn't create job with server dry run", func() {
			ginkgo.By("Create a Job", func() {
				streams, _, out, outErr := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)

				kjobctlCmd := cmd.NewKjobctlCmd(cmd.KjobctlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kjobctlCmd.SetOut(out)
				kjobctlCmd.SetErr(outErr)
				kjobctlCmd.SetArgs([]string{"create", "job", "-n", ns.Name, "--profile", profile.Name, "--dry-run", "server"})

				err := kjobctlCmd.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, out)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
				gomega.Expect(out.String()).Should(gomega.MatchRegexp(
					fmt.Sprintf("job.batch\\/%s-[a-zA-Z0-9]+ created \\(server dry run\\)", profile.Name)))
			})

			ginkgo.By("Check that Job not created", func() {
				jobList := &batchv1.JobList{}
				gomega.Expect(k8sClient.List(ctx, jobList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gomega.Expect(jobList.Items).To(gomega.BeEmpty())
			})
		})
	})
})
