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

package kueuectl

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

func makePassThroughWorkload(ns string) client.Object {
	return testing.MakeWorkload("pass-through-wl", ns).Obj()
}

func makePassThroughLocalQueue(ns string) client.Object {
	return testing.MakeLocalQueue("pass-through-lq", ns).Obj()
}

func makePassThroughClusterQueue(_ string) client.Object {
	return testing.MakeClusterQueue("pass-through-cq").Obj()
}

func makePassThroughResourceFlavor(_ string) client.Object {
	return testing.MakeResourceFlavor("pass-through-resource-flavor").NodeLabel("type", "small").Obj()
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Pass-through commands",
		func(oType string, makeObject func(ns string) client.Object, getPath string, expectGet string, patch string, expectGetAfterPatch string, deleteArgs ...string) {
			obj := makeObject(ns.Name)
			util.MustCreate(ctx, k8sClient, obj)
			key := client.ObjectKeyFromObject(obj)

			identityArgs := []string{oType, key.Name}
			if len(key.Namespace) > 0 {
				identityArgs = append(identityArgs, "-n", key.Namespace)
			}

			ginkgo.By("Get the object", func() {
				args := append([]string{"get"}, identityArgs...)
				args = append(args, fmt.Sprintf("-o=jsonpath='%s'", getPath))
				cmd := exec.Command(kueuectlPath, args...)
				setupEnv(cmd, kassetsPath, kubeconfigPath)
				out, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))
				gomega.Expect(string(out)).To(gomega.BeComparableTo(expectGet))
			})

			ginkgo.By("Patch the object", func() {
				args := append([]string{"patch"}, identityArgs...)
				args = append(args, "--type=merge", "-p", patch)
				cmd := exec.Command(kueuectlPath, args...)
				setupEnv(cmd, kassetsPath, kubeconfigPath)
				out, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))

				args = append([]string{"get"}, identityArgs...)
				args = append(args, fmt.Sprintf("-o=jsonpath='%s'", getPath))
				cmd = exec.Command(kueuectlPath, args...)
				setupEnv(cmd, kassetsPath, kubeconfigPath)
				out, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))
				gomega.Expect(string(out)).To(gomega.BeComparableTo(expectGetAfterPatch))
			})

			ginkgo.By("Delete the object", func() {
				args := append([]string{"delete"}, identityArgs...)
				args = append(args, deleteArgs...)
				cmd := exec.Command(kueuectlPath, args...)
				setupEnv(cmd, kassetsPath, kubeconfigPath)
				out, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, obj)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		},
		ginkgo.Entry("Workload", "workload", makePassThroughWorkload, "{.spec.active}", "'true'", `{"spec":{"active":false}}`, "'false'", "--yes"),
		ginkgo.Entry("Workload(short)", "wl", makePassThroughWorkload, "{.spec.active}", "'true'", `{"spec":{"active":false}}`, "'false'", "--yes"),
		ginkgo.Entry("LocalQueue", "localqueue", makePassThroughLocalQueue, "{.spec.stopPolicy}", "'None'", `{"spec":{"stopPolicy":"Hold"}}`, "'Hold'"),
		ginkgo.Entry("LocalQueue(short)", "lq", makePassThroughLocalQueue, "{.spec.stopPolicy}", "'None'", `{"spec":{"stopPolicy":"Hold"}}`, "'Hold'"),
		ginkgo.Entry("ClusterQueue", "clusterqueue", makePassThroughClusterQueue, "{.spec.stopPolicy}", "'None'", `{"spec":{"stopPolicy":"Hold"}}`, "'Hold'"),
		ginkgo.Entry("ClusterQueue(short)", "cq", makePassThroughClusterQueue, "{.spec.stopPolicy}", "'None'", `{"spec":{"stopPolicy":"Hold"}}`, "'Hold'"),
		ginkgo.Entry("ResourceFlavor", "resourceflavor", makePassThroughResourceFlavor, "{.spec.nodeLabels.type}", "'small'", `{"spec":{"nodeLabels":{"type":"large"}}}`, "'large'"),
		ginkgo.Entry("ResourceFlavor(short)", "rf", makePassThroughResourceFlavor, "{.spec.nodeLabels.type}", "'small'", `{"spec":{"nodeLabels":{"type":"large"}}}`, "'large'"),
	)
})
