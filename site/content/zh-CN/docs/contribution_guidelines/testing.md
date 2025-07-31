---
title: "运行和调试测试"
linkTitle: "运行测试"
weight: 25
description: >
  运行和调试测试
---

## 运行预提交验证测试
```shell
make verify
```

## 运行单元测试
运行所有单元测试：
```shell
make test
```

运行 webhooks 的单元测试：
```shell
go test ./pkg/webhooks
```
运行匹配 `TestValidateClusterQueue` 正则表达式的测试 [参考](https://pkg.go.dev/cmd/go#hdr-Testing_flags)：
```shell
go test ./pkg/webhooks -run TestValidateClusterQueue
```

### 使用竞态检测运行单元测试

使用 `-race` 启用 Go 内置的竞态检测器：
```shell
go test ./pkg/scheduler/preemption/ -race
```

### 使用压力测试运行单元测试

循环运行单元测试并收集失败情况：
```shell
# 安装 go stress
go install golang.org/x/tools/cmd/stress@latest
# 编译测试（可以添加 -race 进行竞态检测）
go test ./pkg/scheduler/preemption/ -c
# 循环运行并报告失败
stress ./preemption.test -test.run TestPreemption
```

## 运行集成测试

```shell
make test-integration
```

关于运行测试子集，请参阅 [运行测试子集](#运行集成或-e2e-测试子集)。

## 使用自定义构建运行 e2e 测试
```shell
make kind-image-build
make test-e2e
make test-tas-e2e
make test-e2e-customconfigs
make test-e2e-certmanager
make test-e2e-kueueviz
make test-multikueue-e2e
```

您可以通过设置 `E2E_K8S_FULL_VERSION` 变量来指定用于运行 e2e 测试的 Kubernetes 版本：
```shell
E2E_K8S_FULL_VERSION=1.33.1 make test-e2e
```

关于运行测试子集，请参阅 [运行测试子集](#运行集成或-e2e-测试子集)。

## 增加日志详细程度
您可以使用 `TEST_LOG_LEVEL` 变量更改日志级别（例如，设置 -5 来增加详细程度）。
默认情况下，`TEST_LOG_LEVEL=-3`。

## 在 VSCode 中调试测试
可以在 VSCode 中调试单元测试和集成测试。
您需要安装 [Go 扩展](https://marketplace.visualstudio.com/items?itemName=golang.Go)。
现在您将在如下行上方看到 `run test | debug test` 文本按钮：
```go
func TestValidateClusterQueue(t *testing.T) {
```
您可以点击 `debug test` 来调试特定测试。

对于集成测试，需要额外的步骤。在 settings.json 中，您需要在 `go.testEnvVars` 内添加两个变量：
- 运行 `ENVTEST_K8S_VERSION=1.33 make envtest && ./bin/setup-envtest use $ENVTEST_K8S_VERSION -p path` 并将路径分配给 `KUBEBUILDER_ASSETS` 变量
- 将 `KUEUE_BIN` 设置为您的 Kueue 仓库克隆目录内的 `bin` 目录
```json
"go.testEnvVars": {
    "KUBEBUILDER_ASSETS": "<上面输出的路径>",
    "KUEUE_BIN": "<您的-kueue-文件夹路径>/bin",
  },
```

对于 e2e 测试，您也可以使用 [Ginkgo Test Explorer](https://marketplace.visualstudio.com/items?itemName=joselitofilho.ginkgotestexplorer)。您需要在 settings.json 中添加以下变量：
```json
 "ginkgotestexplorer.testEnvVars": {
        "KIND_CLUSTER_NAME": "kind",
        "WORKER1_KIND_CLUSTER_NAME": "kind-worker1",
        "MANAGER_KIND_CLUSTER_NAME": "kind-manager",
        "WORKER2_KIND_CLUSTER_NAME": "kind-worker2",
        "KIND": "<您的_kueue_路径>/bin/kind",
    },
```
然后您可以使用 Ginkgo Test Explorer 的 GUI 来运行单个测试，前提是您已经启动了 kind 集群（有关说明，请参阅[这里](#将-e2e-测试附加到现有的-kind-集群)）。

## 将 e2e 测试附加到现有的 kind 集群
您可以使用以下方法来启动 kind 集群，然后从命令行或 VSCode 运行 e2e 测试，
将它们附加到现有集群。例如，假设您想测试一些 multikueue-e2e 测试。

运行 `E2E_RUN_ONLY_ENV=true make kind-image-build test-multikueue-e2e` 并等待 `Press Enter to cleanup.` 出现。

集群已准备就绪，现在您可以从另一个终端运行测试：
```shell
<您的_kueue_路径>/bin/ginkgo --json-report ./ginkgo.report -focus "MultiKueue when Creating a multikueue admission check Should run a jobSet on worker if admitted" -r
```
或从 VSCode 运行。

## 运行集成或 e2e 测试子集
### 使用 Ginkgo --focus 参数
```shell
GINKGO_ARGS="--focus=Scheduler" make test-integration
GINKGO_ARGS="--focus=Creating a Pod requesting TAS" make test-e2e
```
### 使用 ginkgo.FIt
如果您想专注于特定测试，可以将这些测试的
`ginkgo.It` 更改为 `ginkgo.FIt`。
有关更多详细信息，请参阅[这里](https://onsi.github.io/ginkgo/#focused-specs)。
然后其他测试将被跳过。
例如，您可以将
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

### 使用 INTEGRATION_TARGET
```shell
INTEGRATION_TARGET='test/integration/singlecluster/scheduler' make test-integration
```

## 不稳定的集成/e2e 测试
您可以使用 --until-it-fails 或 --repeat=N 参数来让 Ginkgo 重复运行测试，例如：
```shell
GINKGO_ARGS="--until-it-fails" make test-integration
GINKGO_ARGS="--repeat=10" make test-e2e
```
更多信息请参阅[这里](https://onsi.github.io/ginkgo/#repeating-spec-runs-and-managing-flaky-specs)

### 添加压力
您可以运行 [stress](https://github.com/resurrecting-open-source-projects/stress) 工具来在测试期间增加 CPU 负载。例如，如果您在基于 Debian 的 Linux 上：
```shell
# 安装 stress：
sudo apt install stress
# 与测试一起运行 stress
/usr/bin/stress --cpu 80
```

### 分析日志
Kueue 作为常规 pod 在 worker 节点上运行，在 e2e 测试中有 2 个副本在运行。Kueue 日志位于 `kind-worker/pods/kueue-system_kueue-controller-manager*/manager` 和 `kind-worker2/pods/kueue-system_kueue-controller-manager*/manager` 文件夹中。

对于每条日志消息，您可以看到消息来自哪个文件和行：
```log
2025-02-03T15:51:51.502425029Z stderr F 2025-02-03T15:51:51.502117824Z	LEVEL(-2)	cluster-queue-reconciler	core/clusterqueue_controller.go:341	ClusterQueue update event	{"clusterQueue": {"name":"cluster-queue"}}
```
这里，它是 `core/clusterqueue_controller.go:341`。

### 另请参阅
- [Kubernetes 测试指南](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/testing.md)
- [Kubernetes 中的集成测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/integration-tests.md)
- [Kubernetes 中的端到端测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/e2e-tests.md)
- [Kubernetes 中的不稳定测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/flaky-tests.md)
