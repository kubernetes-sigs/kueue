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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

func makePassThroughWorkload(ns string) client.Object {
	return testing.MakeWorkload("pass-through-wl", ns).Obj()
}

func makePassThroughCQ(_ string) client.Object {
	return testing.MakeClusterQueue("pass-through-cq").Obj()
}

func makePassThroughLQ(ns string) client.Object {
	return testing.MakeLocalQueue("pass-through-lq", ns).ClusterQueue("queue").Obj()
}

func setupEnv(c *exec.Cmd, kassetsPath string, kubeconfigPath string) {
	c.Env = os.Environ()
	cmdPath := os.Getenv("PATH")
	if cmdPath == "" {
		cmdPath = kassetsPath
	} else if !set.New(filepath.SplitList(cmdPath)...).Has(kassetsPath) {
		cmdPath = fmt.Sprintf("%s%c%s", kassetsPath, os.PathListSeparator, cmdPath)
	}

	c.Env = append(c.Env, "PATH="+cmdPath)
	c.Env = append(c.Env, "KUBECONFIG="+kubeconfigPath)
}

var _ = ginkgo.Describe("Kueuectl Pass-through", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Pass-through commands",
		func(oType string, namespaced bool, makeObject func(ns string) client.Object, getPath string, expectGet string, patch string, expectGetAfterPatch string) {
			obj := makeObject(ns.Name)
			gomega.Expect(k8sClient.Create(ctx, obj)).To(gomega.Succeed())
			key := client.ObjectKeyFromObject(obj)

			identityArgs := []string{oType, key.Name}
			if namespaced {
				identityArgs = append(identityArgs, "-n", key.Namespace)
			}

			// get it
			args := append([]string{"get"}, identityArgs...)
			args = append(args, fmt.Sprintf("-o=jsonpath='%s'", getPath))
			cmd := exec.Command(kueuectlPath, args...)
			setupEnv(cmd, kassetsPath, kubeconfigPath)
			out, err := cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))
			gomega.Expect(string(out)).To(gomega.BeComparableTo(expectGet))

			// patch it
			if patch != "" {
				args = append([]string{"patch"}, identityArgs...)
				args = append(args, "--type=merge", "-p", patch)
				cmd = exec.Command(kueuectlPath, args...)
				setupEnv(cmd, kassetsPath, kubeconfigPath)
				_, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))

				args = append([]string{"get"}, identityArgs...)
				args = append(args, fmt.Sprintf("-o=jsonpath='%s'", getPath))
				cmd = exec.Command(kueuectlPath, args...)
				setupEnv(cmd, kassetsPath, kubeconfigPath)
				out, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))
				gomega.Expect(string(out)).To(gomega.BeComparableTo(expectGetAfterPatch))
			}
			// delete it
			args = []string{"delete", oType}
			if namespaced {
				args = append(args, "-n", key.Namespace)
			}
			args = append(args, key.Name)
			cmd = exec.Command(kueuectlPath, args...)
			setupEnv(cmd, kassetsPath, kubeconfigPath)
			_, err = cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))

			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, key, obj)
			}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
		},
		ginkgo.Entry("Workload", "workload", true, makePassThroughWorkload, "{.spec.active}", "'true'", `{"spec":{"active":false}}`, "'false'"),
		ginkgo.Entry("Workload(short)", "wl", true, makePassThroughWorkload, "{.spec.active}", "'true'", `{"spec":{"active":false}}`, "'false'"),
		ginkgo.Entry("Cluster Queue", "clusterqueue", false, makePassThroughCQ, "{.spec.stopPolicy}", "'None'", `{"spec":{"stopPolicy":"Hold"}}`, "'Hold'"),
		ginkgo.Entry("Cluster Queue(short)", "cq", false, makePassThroughCQ, "{.spec.stopPolicy}", "'None'", `{"spec":{"stopPolicy":"Hold"}}`, "'Hold'"),
		ginkgo.Entry("Local Queue", "localqueue", true, makePassThroughLQ, "{.spec.clusterQueue}", "'queue'", ``, ""),
		ginkgo.Entry("Local Queue (short)", "lq", true, makePassThroughLQ, "{.spec.clusterQueue}", "'queue'", ``, ""),
	)
})
