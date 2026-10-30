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

	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// setupEnv configures environment variables including PATH, KUBECONFIG, and any additional variables for the command.
func setupEnv(c *exec.Cmd, kassetsPath string, kubeconfigPath string, extraEnv ...string) {
	c.Env = os.Environ()
	cmdPath := os.Getenv("PATH")
	if cmdPath == "" {
		cmdPath = kassetsPath
	} else if !set.New(filepath.SplitList(cmdPath)...).Has(kassetsPath) {
		cmdPath = fmt.Sprintf("%s%c%s", kassetsPath, os.PathListSeparator, cmdPath)
	}

	c.Env = append(c.Env, "PATH="+cmdPath)
	c.Env = append(c.Env, "KUBECONFIG="+kubeconfigPath)
	c.Env = append(c.Env, extraEnv...)
}

func runKueuectl(args ...string) string {
	ginkgo.GinkgoHelper()
	return runKueuectlWithEnv(nil, args...)
}

func runKueuectlWithEnv(extraEnv []string, args ...string) string {
	ginkgo.GinkgoHelper()
	cmd := exec.Command(kueuectlPath, args...)
	setupEnv(cmd, kassetsPath, kubeconfigPath, extraEnv...)
	out, err := cmd.CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%q", string(out))
	return string(out)
}

var _ = ginkgo.Describe("Kueuectl Pass-through", func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should get, describe, patch, edit and delete a Workload", framework.SlowSpec, func() {
		wl := utiltestingapi.MakeWorkload("pass-through-wl", ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
		})

		for _, name := range []string{"workload", "kwl", "kueueworkload", "kueueworkloads"} {
			gomega.Expect(runKueuectl("get", name, wl.Name, "-n", wl.Namespace, "-o=jsonpath={.spec.active}")).To(gomega.Equal("true"))
		}
		gomega.Expect(runKueuectl("describe", "workload", wl.Name, "-n", wl.Namespace)).To(gomega.MatchRegexp(`Active:\s+true`))
		runKueuectl("patch", "workload", wl.Name, "-n", wl.Namespace, "--type=merge", "-p", `{"spec":{"active":false}}`)
		gomega.Expect(runKueuectl("get", "workload", wl.Name, "-n", wl.Namespace, "-o=jsonpath={.spec.active}")).To(gomega.Equal("false"))
		runKueuectlWithEnv([]string{`KUBE_EDITOR=sed -i -e 's/active: false/active: true/'`}, "edit", "workload", wl.Name, "-n", wl.Namespace)
		gomega.Expect(runKueuectl("get", "workload", wl.Name, "-n", wl.Namespace, "-o=jsonpath={.spec.active}")).To(gomega.Equal("true"))
		runKueuectl("delete", "kwl", wl.Name, "-n", wl.Namespace, "--yes")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, false)
	})

	ginkgo.It("Should get, describe, patch, edit and delete a LocalQueue", func() {
		lq := utiltestingapi.MakeLocalQueue("pass-through-lq", ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		})

		for _, name := range []string{"localqueue", "lq"} {
			gomega.Expect(runKueuectl("get", name, lq.Name, "-n", lq.Namespace, "-o=jsonpath={.spec.stopPolicy}")).To(gomega.Equal("None"))
		}
		gomega.Expect(runKueuectl("describe", "localqueue", lq.Name, "-n", lq.Namespace)).To(gomega.MatchRegexp(`Stop Policy:\s+None`))
		runKueuectl("patch", "localqueue", lq.Name, "-n", lq.Namespace, "--type=merge", "-p", `{"spec":{"stopPolicy":"Hold"}}`)
		gomega.Expect(runKueuectl("get", "localqueue", lq.Name, "-n", lq.Namespace, "-o=jsonpath={.spec.stopPolicy}")).To(gomega.Equal("Hold"))
		runKueuectlWithEnv([]string{`KUBE_EDITOR=sed -i -e 's/stopPolicy: Hold/stopPolicy: None/'`}, "edit", "localqueue", lq.Name, "-n", lq.Namespace)
		gomega.Expect(runKueuectl("get", "localqueue", lq.Name, "-n", lq.Namespace, "-o=jsonpath={.spec.stopPolicy}")).To(gomega.Equal("None"))
		runKueuectl("delete", "localqueue", lq.Name, "-n", lq.Namespace)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, false)
	})

	ginkgo.It("Should get, describe, patch, edit and delete a ClusterQueue", func() {
		cq := utiltestingapi.MakeClusterQueue("pass-through-cq").Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		for _, name := range []string{"clusterqueue", "cq"} {
			gomega.Expect(runKueuectl("get", name, cq.Name, "-o=jsonpath={.spec.stopPolicy}")).To(gomega.Equal("None"))
		}
		gomega.Expect(runKueuectl("describe", "clusterqueue", cq.Name)).To(gomega.MatchRegexp(`Stop Policy:\s+None`))
		runKueuectl("patch", "clusterqueue", cq.Name, "--type=merge", "-p", `{"spec":{"stopPolicy":"Hold"}}`)
		gomega.Expect(runKueuectl("get", "clusterqueue", cq.Name, "-o=jsonpath={.spec.stopPolicy}")).To(gomega.Equal("Hold"))
		runKueuectlWithEnv([]string{`KUBE_EDITOR=sed -i -e 's/stopPolicy: Hold/stopPolicy: None/'`}, "edit", "clusterqueue", cq.Name)
		gomega.Expect(runKueuectl("get", "clusterqueue", cq.Name, "-o=jsonpath={.spec.stopPolicy}")).To(gomega.Equal("None"))
		runKueuectl("delete", "clusterqueue", cq.Name)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, false)
	})

	ginkgo.It("Should get, describe, patch, edit and delete a ResourceFlavor", func() {
		rf := utiltestingapi.MakeResourceFlavor("pass-through-resource-flavor").NodeLabel("type", "small").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		})

		for _, name := range []string{"resourceflavor", "rf"} {
			gomega.Expect(runKueuectl("get", name, rf.Name, "-o=jsonpath={.spec.nodeLabels.type}")).To(gomega.Equal("small"))
		}
		gomega.Expect(runKueuectl("describe", "resourceflavor", rf.Name)).To(gomega.MatchRegexp(`Type:\s+small`))
		runKueuectl("patch", "resourceflavor", rf.Name, "--type=merge", "-p", `{"spec":{"nodeLabels":{"type":"large"}}}`)
		gomega.Expect(runKueuectl("get", "resourceflavor", rf.Name, "-o=jsonpath={.spec.nodeLabels.type}")).To(gomega.Equal("large"))
		runKueuectlWithEnv([]string{`KUBE_EDITOR=sed -i -e 's/type: large/type: small/'`}, "edit", "resourceflavor", rf.Name)
		gomega.Expect(runKueuectl("get", "resourceflavor", rf.Name, "-o=jsonpath={.spec.nodeLabels.type}")).To(gomega.Equal("small"))
		runKueuectl("delete", "resourceflavor", rf.Name)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, false)
	})

	ginkgo.It("Should get, describe, patch, edit and delete a Cohort", func() {
		cohort := utiltestingapi.MakeCohort("pass-through-cohort").Parent("parent-1").Obj()
		behavioral.MustCreate(ctx, k8sClient, cohort)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		})

		for _, name := range []string{"cohort", "co"} {
			gomega.Expect(runKueuectl("get", name, cohort.Name, "-o=jsonpath={.spec.parentName}")).To(gomega.Equal("parent-1"))
		}
		gomega.Expect(runKueuectl("describe", "cohort", cohort.Name)).To(gomega.MatchRegexp(`Parent Name:\s+parent-1`))
		runKueuectl("patch", "cohort", cohort.Name, "--type=merge", "-p", `{"spec":{"parentName":"parent-2"}}`)
		gomega.Expect(runKueuectl("get", "cohort", cohort.Name, "-o=jsonpath={.spec.parentName}")).To(gomega.Equal("parent-2"))
		runKueuectlWithEnv([]string{`KUBE_EDITOR=sed -i -e 's/parentName: parent-2/parentName: parent-1/'`}, "edit", "cohort", cohort.Name)
		gomega.Expect(runKueuectl("get", "cohort", cohort.Name, "-o=jsonpath={.spec.parentName}")).To(gomega.Equal("parent-1"))
		runKueuectl("delete", "cohort", cohort.Name)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, false)
	})

	ginkgo.It("Should get, describe, patch, edit and delete a Topology", func() {
		topo := utiltestingapi.MakeDefaultThreeLevelTopology("pass-through-topology")
		behavioral.MustCreate(ctx, k8sClient, topo)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topo, true)
		})

		for _, name := range []string{"topology", "topo"} {
			gomega.Expect(runKueuectl("get", name, topo.Name, "-o=jsonpath={.spec.levels[0].nodeLabel}")).To(gomega.Equal("cloud.provider.com/topology-block"))
		}
		gomega.Expect(runKueuectl("describe", "topology", topo.Name)).To(gomega.MatchRegexp(`Node Label:\s+cloud.provider.com/topology-block`))
		runKueuectl(
			"patch",
			"topology",
			topo.Name,
			"--type=merge",
			"-p",
			`{"spec":{"levels":[{"nodeLabel":"cloud.provider.com/topology-rack"},{"nodeLabel":"cloud.provider.com/topology-block"},{"nodeLabel":"kubernetes.io/hostname"}]}}`,
		)
		gomega.Expect(runKueuectl("get", "topology", topo.Name, "-o=jsonpath={.spec.levels[0].nodeLabel}")).To(gomega.Equal("cloud.provider.com/topology-rack"))
		runKueuectlWithEnv([]string{`KUBE_EDITOR=sed -i -e 's/topology-rack/tmp/' -e 's/topology-block/topology-rack/' -e 's/tmp/topology-block/'`}, "edit", "topology", topo.Name)
		gomega.Expect(runKueuectl("get", "topology", topo.Name, "-o=jsonpath={.spec.levels[0].nodeLabel}")).To(gomega.Equal("cloud.provider.com/topology-block"))
		runKueuectl("delete", "topology", topo.Name)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topo, false)
	})
})
