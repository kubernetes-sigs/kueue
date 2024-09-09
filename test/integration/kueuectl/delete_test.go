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

package kueuectl

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	bactchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

// Current version of go-client doesn't allow to use SimpleDynamicClient.
// TODO: Move this tests to unit after merge https://github.com/kubernetes-sigs/kueue/pull/2402.
var _ = ginkgo.Describe("Kueuectl Delete", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns   *corev1.Namespace
		wl1  *v1beta1.Workload
		wl2  *v1beta1.Workload
		wl3  *v1beta1.Workload
		job1 *bactchv1.Job
		job2 *bactchv1.Job
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		job1 = testingjob.MakeJob("job1", ns.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, job1)).To(gomega.Succeed())
		job2 = testingjob.MakeJob("job2", ns.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, job2)).To(gomega.Succeed())

		jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

		wl1 = testing.MakeWorkload("wl1", ns.Name).OwnerReference(jobGVK, job1.Name, string(job1.UID)).Obj()
		gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())
		wl2 = testing.MakeWorkload("wl2", ns.Name).OwnerReference(jobGVK, job2.Name, string(job2.UID)).Obj()
		gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())
		wl3 = testing.MakeWorkload("wl3", ns.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, wl3)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Deleting a Workload", func() {
		ginkgo.It("Shouldn't delete a Workload because not confirmed", func() {
			ginkgo.By("Delete a Workload", func() {
				streams, input, output, errOutput := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetIn(input)
				kueuectl.SetOut(output)
				kueuectl.SetErr(errOutput)

				input.WriteString("n\n")

				kueuectl.SetArgs([]string{"delete", "workload", wl1.Name, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Expect(output.String()).To(gomega.Equal(`This operation will also delete:
  - jobs.batch/job1 associated with the wl1 workload
Do you want to proceed (y/n)? Deletion is canceled
`))
				gomega.Expect(errOutput.String()).To(gomega.Equal(""))
			})

			ginkgo.By("Check that the Workload and its associated Job were not deleted", func() {
				createdJob1 := &bactchv1.Job{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job1), createdJob1)).To(gomega.Succeed())

				createdWl1 := &v1beta1.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), createdWl1)).To(gomega.Succeed())
			})
		})

		ginkgo.It("Shouldn't delete a Workload because it is not found", func() {
			ginkgo.By("Delete a Workload", func() {
				streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(errOutput)

				kueuectl.SetArgs([]string{"delete", "workload", "foo", "--namespace", ns.Name, "--yes"})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Expect(output.String()).To(gomega.Equal(""))
				gomega.Expect(errOutput.String()).To(gomega.Equal("workloads.kueue.x-k8s.io \"foo\" not found\n"))
			})
		})

		ginkgo.It("Should delete a Workload and its corresponding Job", func() {
			ginkgo.By("Delete a Workload", func() {
				streams, input, output, errOutput := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetIn(input)
				kueuectl.SetOut(output)
				kueuectl.SetErr(errOutput)

				input.WriteString("y\n")

				kueuectl.SetArgs([]string{"delete", "workload", wl1.Name, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Expect(output.String()).To(gomega.Equal(`This operation will also delete:
  - jobs.batch/job1 associated with the wl1 workload
Do you want to proceed (y/n)? workload.kueue.x-k8s.io/wl1 deleted
`))
				gomega.Expect(errOutput.String()).To(gomega.Equal(""))
			})

			ginkgo.By("Check that the Workload and its corresponding Job were successfully deleted", func() {
				createdJob1 := &bactchv1.Job{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job1), createdJob1)).To(testing.BeNotFoundError())

				createdWl1 := &v1beta1.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), createdWl1)).To(testing.BeNotFoundError())
			})
		})

		ginkgo.It("Should delete the Workloads and the Jobs corresponding to them", func() {
			ginkgo.By("Delete Workloads", func() {
				streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(errOutput)

				kueuectl.SetArgs([]string{"delete", "workload", wl1.Name, wl2.Name, "--namespace", ns.Name, "--yes"})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Expect(output.String()).To(gomega.Equal(`workload.kueue.x-k8s.io/wl1 deleted
workload.kueue.x-k8s.io/wl2 deleted
`))
				gomega.Expect(errOutput.String()).To(
					gomega.Equal(""))
			})

			ginkgo.By("Check that the Workloads and their corresponding Jobs were successfully deleted", func() {
				createdJob1 := &bactchv1.Job{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job1), createdJob1)).To(testing.BeNotFoundError())
				createdJob2 := &bactchv1.Job{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job2), createdJob2)).To(testing.BeNotFoundError())

				createdWl1 := &v1beta1.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), createdWl1)).To(testing.BeNotFoundError())
				createdWl2 := &v1beta1.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), createdWl2)).To(testing.BeNotFoundError())
			})
		})

		ginkgo.It("Should delete only a Workload because there are no OwnerReferences", func() {
			ginkgo.By("Delete a Workload", func() {
				streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(errOutput)

				kueuectl.SetArgs([]string{"delete", "workload", wl3.Name, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Expect(output.String()).To(gomega.Equal("workload.kueue.x-k8s.io/wl3 deleted\n"))
				gomega.Expect(errOutput.String()).To(gomega.Equal(""))
			})

			ginkgo.By("Check that the Workload and its corresponding Job were successfully deleted", func() {
				createdWl3 := &v1beta1.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), createdWl3)).To(testing.BeNotFoundError())
			})
		})
	})
})
