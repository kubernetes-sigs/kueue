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

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kueue/pkg/util/maps"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/test/util"
)

const (
	BatchJobNameLabel            = "batch.kubernetes.io/job-name"
	BatchJobCompletionIndexLabel = "batch.kubernetes.io/job-completion-index"
)

var _ = ginkgo.Describe("Slurm", ginkgo.Ordered, func() {
	var (
		ns          *corev1.Namespace
		profile     *v1alpha1.ApplicationProfile
		jobTemplate *v1alpha1.JobTemplate
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-slurm-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		jobTemplate = wrappers.MakeJobTemplate("job-template", ns.Name).
			RestartPolicy(corev1.RestartPolicyNever).
			BackoffLimitPerIndex(0).
			WithContainer(*wrappers.MakeContainer("c1", util.E2eTestBashImage).Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, jobTemplate)).To(gomega.Succeed())

		profile = wrappers.MakeApplicationProfile("profile", ns.Name).
			WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "job-template").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, profile)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("should be created", func(
		args, slurmArgs []string,
		expectCompletions, expectParallelism int32,
		expectCommonVars map[string]string, expectPods []map[string]map[string]string,
		withFirstNodeIP bool,
	) {
		ginkgo.By("Create temporary file")
		script, err := os.CreateTemp("", "e2e-slurm-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer script.Close()
		defer os.Remove(script.Name())

		ginkgo.By("Prepare script", func() {
			_, err := script.WriteString("#!/bin/bash\nwhile true; do printenv | grep SLURM_ > /env.out; sleep 0.25; done")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		var jobName, configMapName, serviceName string

		ginkgo.By("Create slurm", func() {
			cmdArgs := []string{"create", "slurm", "-n", ns.Name, "--profile", profile.Name}
			cmdArgs = append(cmdArgs, args...)
			cmdArgs = append(cmdArgs, "--", script.Name())
			cmdArgs = append(cmdArgs, slurmArgs...)

			cmd := exec.Command(kjobctlPath, cmdArgs...)
			out, err := util.Run(cmd)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, out)
			gomega.Expect(out).NotTo(gomega.BeEmpty())

			jobName, configMapName, serviceName, err = parseSlurmCreateOutput(out, profile.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(jobName).NotTo(gomega.BeEmpty())
			gomega.Expect(configMapName).NotTo(gomega.BeEmpty())
			gomega.Expect(serviceName).NotTo(gomega.BeEmpty())
		})

		job := &batchv1.Job{}
		configMap := &corev1.Service{}
		service := &corev1.Service{}

		ginkgo.By("Check slurm is created", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: jobName}, job)).To(gomega.Succeed())
			gomega.Expect(ptr.Deref(job.Spec.Completions, 1)).To(gomega.Equal(expectCompletions))
			gomega.Expect(ptr.Deref(job.Spec.Parallelism, 1)).To(gomega.Equal(expectParallelism))

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: configMapName}, configMap)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: serviceName}, service)).To(gomega.Succeed())
		})

		var firstPod *corev1.Pod

		for completionIndex, expectPod := range expectPods {
			podList := &corev1.PodList{}

			ginkgo.By(fmt.Sprintf("Wait for pod with completion index %d is running", completionIndex), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name), client.MatchingLabels(map[string]string{
						BatchJobNameLabel:            job.Name,
						BatchJobCompletionIndexLabel: strconv.Itoa(completionIndex),
					}))).Should(gomega.Succeed())
					g.Expect(podList.Items).Should(gomega.HaveLen(1))
					g.Expect(podList.Items[0].Status.Phase).To(gomega.Equal(corev1.PodRunning))
					if completionIndex == 0 {
						firstPod = &podList.Items[0]
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pod := podList.Items[0]

			for containerName, expectContainerVars := range expectPod {
				ginkgo.By(fmt.Sprintf("Check env variables in index %d and container name %s", completionIndex, containerName), func() {
					wantOut := maps.MergeKeepFirst(expectContainerVars, expectCommonVars)
					if withFirstNodeIP {
						wantOut["SLURM_JOB_FIRST_NODE_IP"] = firstPod.Status.PodIP
					}

					gomega.Eventually(func(g gomega.Gomega) {
						out, outErr, err := util.KExecute(ctx, cfg, restClient, ns.Name, pod.Name, containerName)
						g.Expect(err).NotTo(gomega.HaveOccurred())
						g.Expect(string(outErr)).To(gomega.BeEmpty())
						g.Expect(parseSlurmEnvOutput(out)).To(gomega.BeComparableTo(wantOut,
							cmpopts.AcyclicTransformer("RemoveGeneratedNameSuffixInMap", func(m map[string]string) map[string]string {
								for key, val := range m {
									m[key] = regexp.MustCompile("(profile-slurm)(-.{5})").ReplaceAllString(val, "$1")
								}
								return m
							}),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			}

			if completionIndex != 0 || !withFirstNodeIP {
				gomega.Expect(k8sClient.Delete(ctx, &pod)).To(gomega.Succeed())
			}
		}
	},
		ginkgo.Entry(
			"without arguments",
			[]string{}, []string{},
			int32(1), int32(1),
			map[string]string{
				"SLURM_NTASKS_PER_NODE":   "1",
				"SLURM_ARRAY_JOB_ID":      "1",
				"SLURM_MEM_PER_CPU":       "",
				"SLURM_GPUS":              "",
				"SLURM_NNODES":            "1",
				"SLURM_MEM_PER_GPU":       "",
				"SLURM_NTASKS":            "1",
				"SLURM_ARRAY_TASK_COUNT":  "1",
				"SLURM_TASKS_PER_NODE":    "1",
				"SLURM_CPUS_PER_TASK":     "",
				"SLURM_ARRAY_TASK_MAX":    "0",
				"SLURM_CPUS_PER_GPU":      "",
				"SLURM_SUBMIT_DIR":        "/slurm/scripts",
				"SLURM_NPROCS":            "1",
				"SLURM_CPUS_ON_NODE":      "",
				"SLURM_ARRAY_TASK_MIN":    "0",
				"SLURM_JOB_NODELIST":      "profile-slurm-xxxxx-0.profile-slurm-xxxxx",
				"SLURM_JOB_CPUS_PER_NODE": "",
				"SLURM_JOB_FIRST_NODE":    "profile-slurm-xxxxx-1.profile-slurm-xxxxx",
				"SLURM_MEM_PER_NODE":      "",
				"SLURM_JOB_FIRST_NODE_IP": "",
			},
			[]map[string]map[string]string{
				{
					"c1": {
						"SLURM_ARRAY_TASK_ID": "0",
						"SLURM_JOB_ID":        "1",
						"SLURM_JOBID":         "1",
						"SLURM_SUBMIT_HOST":   "profile-slurm-xxxxx-0",
					},
				},
			},
			false,
		),
		ginkgo.Entry(
			"with --first-node-ip",
			[]string{"--first-node-ip"}, []string{"--array", "1-5%2", "--nodes", "2", "--ntasks", "2"},
			int32(3), int32(2),
			map[string]string{
				"SLURM_NTASKS_PER_NODE":   "2",
				"SLURM_ARRAY_JOB_ID":      "1",
				"SLURM_MEM_PER_CPU":       "",
				"SLURM_GPUS":              "",
				"SLURM_NNODES":            "2",
				"SLURM_MEM_PER_GPU":       "",
				"SLURM_NTASKS":            "2",
				"SLURM_ARRAY_TASK_COUNT":  "5",
				"SLURM_TASKS_PER_NODE":    "2",
				"SLURM_CPUS_PER_TASK":     "",
				"SLURM_ARRAY_TASK_MAX":    "5",
				"SLURM_CPUS_PER_GPU":      "",
				"SLURM_SUBMIT_DIR":        "/slurm/scripts",
				"SLURM_NPROCS":            "2",
				"SLURM_CPUS_ON_NODE":      "",
				"SLURM_ARRAY_TASK_MIN":    "1",
				"SLURM_JOB_NODELIST":      "profile-slurm-xxxxx-0.profile-slurm-xxxxx,profile-slurm-xxxxx-1.profile-slurm-xxxxx",
				"SLURM_JOB_CPUS_PER_NODE": "",
				"SLURM_JOB_FIRST_NODE":    "profile-slurm-xxxxx-1.profile-slurm-xxxxx",
				"SLURM_MEM_PER_NODE":      "",
				"SLURM_JOB_FIRST_NODE_IP": "",
			},
			[]map[string]map[string]string{
				{
					"c1-0": {
						"SLURM_ARRAY_TASK_ID": "1",
						"SLURM_JOB_ID":        "1",
						"SLURM_JOBID":         "1",
						"SLURM_SUBMIT_HOST":   "profile-slurm-xxxxx-0",
					},
					"c1-1": {
						"SLURM_ARRAY_TASK_ID": "2",
						"SLURM_JOB_ID":        "2",
						"SLURM_JOBID":         "2",
						"SLURM_SUBMIT_HOST":   "profile-slurm-xxxxx-0",
					},
				},
				{
					"c1-0": {
						"SLURM_ARRAY_TASK_ID": "3",
						"SLURM_JOB_ID":        "3",
						"SLURM_JOBID":         "3",
						"SLURM_SUBMIT_HOST":   "profile-slurm-xxxxx-1",
					},
					"c1-1": {
						"SLURM_ARRAY_TASK_ID": "4",
						"SLURM_JOB_ID":        "4",
						"SLURM_JOBID":         "4",
						"SLURM_SUBMIT_HOST":   "profile-slurm-xxxxx-1",
					},
				},
				{
					"c1-0": {
						"SLURM_ARRAY_TASK_ID": "5",
						"SLURM_JOB_ID":        "5",
						"SLURM_JOBID":         "5",
						"SLURM_SUBMIT_HOST":   "profile-slurm-xxxxx-2",
					},
				},
			},
			true,
		),
	)

	ginkgo.When("delete", func() {
		ginkgo.It("should delete job and child objects", func() {
			ginkgo.By("Create temporary file")
			script, err := os.CreateTemp("", "e2e-slurm-")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer script.Close()
			defer os.Remove(script.Name())

			ginkgo.By("Prepare script", func() {
				_, err := script.WriteString("#!/bin/bash\nsleep 600")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			})

			var jobName, configMapName, serviceName string

			ginkgo.By("Create slurm", func() {
				cmdArgs := []string{"create", "slurm", "-n", ns.Name, "--profile", profile.Name}
				cmdArgs = append(cmdArgs, "--", script.Name())

				cmd := exec.Command(kjobctlPath, cmdArgs...)
				out, err := util.Run(cmd)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, out)
				gomega.Expect(out).NotTo(gomega.BeEmpty())

				jobName, configMapName, serviceName, err = parseSlurmCreateOutput(out, profile.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(jobName).NotTo(gomega.BeEmpty())
				gomega.Expect(configMapName).NotTo(gomega.BeEmpty())
				gomega.Expect(serviceName).NotTo(gomega.BeEmpty())
			})

			job := &batchv1.Job{}
			configMap := &corev1.Service{}
			service := &corev1.Service{}

			ginkgo.By("Check slurm is created", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: jobName}, job)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: configMapName}, configMap)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: serviceName}, service)).To(gomega.Succeed())
			})

			ginkgo.By("Delete slurm", func() {
				cmd := exec.Command(kjobctlPath, "delete", "slurm", "-n", ns.Name, jobName)
				out, err := util.Run(cmd)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, out)
				gomega.Expect(string(out)).To(gomega.Equal(fmt.Sprintf("job.batch/%s deleted\n", jobName)))
			})

			ginkgo.By("Check job and child objects are deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(errors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job))).To(gomega.BeTrue())
					g.Expect(errors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(configMap), configMap))).To(gomega.BeTrue())
					g.Expect(errors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(service), service))).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

func parseSlurmCreateOutput(output []byte, profileName string) (string, string, string, error) {
	output = bytes.ReplaceAll(output, []byte("\n"), []byte(""))
	re := regexp.MustCompile(fmt.Sprintf(`^job\.batch\/(%[1]s-slurm-.{5}) createdconfigmap\/(%[1]s-slurm-.{5}) createdservice\/(%[1]s-slurm-.{5}) created$`, profileName))
	matches := re.FindSubmatch(output)

	if len(matches) < 4 {
		return "", "", "", fmt.Errorf("unexpected output format: %s", output)
	}

	return string(matches[1]), string(matches[2]), string(matches[3]), nil
}

func parseSlurmEnvOutput(output []byte) map[string]string {
	parts := bytes.Split(output, []byte{'\n'})
	gotOut := make(map[string]string, len(parts))
	for _, part := range parts {
		pair := bytes.SplitN(part, []byte{'='}, 2)
		if len(pair) == 2 {
			gotOut[string(pair[0])] = string(pair[1])
		}
	}
	return gotOut
}
