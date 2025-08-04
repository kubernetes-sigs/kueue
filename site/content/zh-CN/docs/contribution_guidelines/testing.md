---
title: "运行和调试测试"
linkTitle: "运行测试"
weight: 25
description: >
  运行和调试测试
---

## 运行预提交验证测试 {#running_presubmission_verification_tests}
```shell
make verify
```

## 运行单元测试 {#running_unit_tests}
运行所有单元测试：
```shell
make test
```

运行 webhook 的单元测试：
```shell
go test ./pkg/webhooks
```
运行匹配 `TestValidateClusterQueue` 正则表达式的测试[引用](https://pkg.go.dev/cmd/go#hdr-Testing_flags)：
```shell
go test ./pkg/webhooks -run TestValidateClusterQueue
```

### 使用竞态检测运行单元测试 {#running_unit_tests_with_race_detection}

使用 `-race` 启用 Go 内置的竞态检测器：
```shell
go test ./pkg/scheduler/preemption/ -race
```

### 使用压力测试运行单元测试 {#running_unit_tests_with_stress}

在循环中运行单元测试并收集失败：
```shell
# install go stress
go install golang.org/x/tools/cmd/stress@latest
# compile tests (you can add -race for race detection)
go test ./pkg/scheduler/preemption/ -c
# it loops and reports failures
stress ./preemption.test -test.run TestPreemption
```

## 运行集成测试 {#running_integration_tests}

```shell
make test-integration
```

关于运行测试子集，请参阅[运行测试子集](#running-subset-of-integration-or-e2e-tests)。

## 使用自定义构建运行 e2e 测试 {#running_e2e_tests_using_custom_build}
```shell
make kind-image-build
make test-e2e
make test-tas-e2e
make test-e2e-customconfigs
make test-e2e-certmanager
make test-e2e-kueueviz
make test-multikueue-e2e
```

你可以通过设置 `E2E_K8S_FULL_VERSION` 变量来指定用于运行 e2e 测试的 Kubernetes 版本：
```shell
E2E_K8S_FULL_VERSION=1.33.1 make test-e2e
```

关于运行测试子集，请参阅[运行测试子集](#running-subset-of-integration-or-e2e-tests)。

## 增加日志详细程度 {#increase_logging_verbosity}
你可以使用 `TEST_LOG_LEVEL` 变量更改日志级别（例如，设置 -5 来增加详细程度）。
默认情况下，`TEST_LOG_LEVEL=-3`。

## 在 VSCode 中调试测试 {#debug_tests_in_vscode}
可以在 VSCode 中调试单元测试和集成测试。
你需要安装 [Go 扩展](https://marketplace.visualstudio.com/items?itemName=golang.Go)。
现在你会在类似这样的行上方看到 `run test | debug test` 文本按钮：
```go
func TestValidateClusterQueue(t *testing.T) {
```
你可以点击 `debug test` 来调试特定的测试。

对于集成测试，需要额外的步骤。在 settings.json 中，你需要在 `go.testEnvVars` 内添加两个变量：
- 运行 `ENVTEST_K8S_VERSION=1.33 make envtest && ./bin/setup-envtest use $ENVTEST_K8S_VERSION -p path` 并将路径分配给 `KUBEBUILDER_ASSETS` 变量
- 将 `KUEUE_BIN` 设置为你的 Kueue 仓库克隆目录内的 `bin` 目录
```json
"go.testEnvVars": {
    "KUBEBUILDER_ASSETS": "<path from output above>",
    "KUEUE_BIN": "<path-to-your-kueue-folder>/bin",
  },
```

对于 e2e 测试，你也可以使用 [Ginkgo Test Explorer](https://marketplace.visualstudio.com/items?itemName=joselitofilho.ginkgotestexplorer)。你需要在 settings.json 中添加以下变量：
```json
 "ginkgotestexplorer.testEnvVars": {
        "KIND_CLUSTER_NAME": "kind",
        "WORKER1_KIND_CLUSTER_NAME": "kind-worker1",
        "MANAGER_KIND_CLUSTER_NAME": "kind-manager",
        "WORKER2_KIND_CLUSTER_NAME": "kind-worker2",
        "KIND": "<your_kueue_path>/bin/kind",
    },
```
然后你可以使用 Ginkgo Test Explorer 的 GUI 来运行单个测试，前提是你已经启动了 kind 集群（有关说明，请参阅[这里](#attaching-e2e-tests-to-an-existing-kind-cluster)）。

## 将 e2e 测试附加到现有的 kind 集群 {#attaching_e2e_tests_to_an_existing_kind_cluster}
你可以使用以下方法来启动 kind 集群，然后从命令行或 VSCode 运行 e2e 测试，
将它们附加到现有集群。例如，假设你想测试一些 multikueue-e2e 测试。

运行 `E2E_RUN_ONLY_ENV=true make kind-image-build test-multikueue-e2e` 并等待 `Press Enter to cleanup.` 出现。

集群已准备就绪，现在你可以从另一个终端运行测试：
```shell
<your_kueue_path>/bin/ginkgo --json-report ./ginkgo.report -focus "MultiKueue when Creating a multikueue admission check Should run a jobSet on worker if admitted" -r
```
或从 VSCode 运行。

## 运行集成或 e2e 测试子集 {#running_subset_of_integration_or_e2e_tests}
### 使用 Ginkgo --focus 参数 {#use_ginkgo_focus_arg}
```shell
GINKGO_ARGS="--focus=Scheduler" make test-integration
GINKGO_ARGS="--focus=Creating a Pod requesting TAS" make test-e2e
```
### 使用 ginkgo.FIt {#use_ginkgo_fit}
如果你想专注于特定测试，可以将这些测试的
`ginkgo.It` 更改为 `ginkgo.FIt`。
有关更多详细信息，请参阅[这里](https://onsi.github.io/ginkgo/#focused-specs)。
然后其他测试将被跳过。
例如，你可以将
```go
ginkgo.It("Should place pods based on the ranks-ordering", func() {
```
更改为
```go
ginkgo.FIt("Should place pods based on the ranks-ordering", func() {
```
然后运行
```shell
# 构建并拉取镜像
make test-tas-e2e
```
来测试特定的 TAS e2e 测试。

### 使用 INTEGRATION_TARGET {#use_integration_target}
```shell
INTEGRATION_TARGET='test/integration/singlecluster/scheduler' make test-integration
```

## 不稳定的集成/e2e 测试 {#flaky_integration_e2e_tests}
你可以使用 --until-it-fails 或 --repeat=N 参数给 Ginkgo 来重复运行测试，例如：
```shell
GINKGO_ARGS="--until-it-fails" make test-integration
GINKGO_ARGS="--repeat=10" make test-e2e
```
更多信息请参阅[这里](https://onsi.github.io/ginkgo/#repeating-spec-runs-and-managing-flaky-specs)

### 添加压力 {#adding_stress}
你可以运行 [stress](https://github.com/resurrecting-open-source-projects/stress)工具来在测试期间增加 CPU 负载。例如，如果你在基于 Debian 的 Linux 上：
```shell
# install stress:
sudo apt install stress
# run stress alongside tests
/usr/bin/stress --cpu 80
```

### 分析日志 {#analyzing_logs}
Kueue 作为常规 Pod 在 worker 节点上运行，在 e2e 测试中有 2 个副本在运行。Kueue 日志位于 `kind-worker/pods/kueue-system_kueue-controller-manager*/manager` 和 `kind-worker2/pods/kueue-system_kueue-controller-manager*/manager` 文件夹中。

对于每条日志消息，你可以看到消息来自哪个文件和行：
```log
2025-02-03T15:51:51.502425029Z stderr F 2025-02-03T15:51:51.502117824Z	LEVEL(-2)	cluster-queue-reconciler	core/clusterqueue_controller.go:341	ClusterQueue update event	{"ClusterQueue": {"name":"cluster-queue"}}
```
这里，它是 `core/clusterqueue_controller.go:341`。

### 另请参阅 {#see-also}
- [Kubernetes 测试指南](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/testing.md)
- [Kubernetes 中的集成测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/integration-tests.md)
- [Kubernetes 中的端到端测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/e2e-tests.md)
- [Kubernetes 中的不稳定测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/flaky-tests.md)
