---
title: "运行和调试测试"
linkTitle: "运行测试"
weight: 25
description: >
  运行和调试测试
type: docs
---

## 快速入门 {#quick-start}

最常用的开发测试流程：

```shell
# 单元测试
make test

# 集成测试
make test-integration

# E2E 测试：构建镜像并运行（Apple Silicon / arm64——amd64 无需设置 PLATFORM）
PLATFORM=linux/arm64 make kind-image-build test-e2e-baseline

# E2E 测试：构建、运行，并在两次运行之间保留 kind 集群
E2E_MODE=dev PLATFORM=linux/arm64 make kind-image-build test-e2e-baseline
```

### 快速过滤参考 {#quick-filter-reference}

```shell
# 按名称模式聚焦测试
GINKGO_ARGS="--focus=Scheduler" make test-integration
GINKGO_ARGS="--focus='Creating a Pod requesting TAS'" make test-e2e-baseline

# 按标签过滤集成测试
INTEGRATION_FILTERS="--label-filter=controller:workload" make test-integration
INTEGRATION_FILTERS="--label-filter=area:jobs" make test-integration

# 按标签过滤 e2e 测试
GINKGO_ARGS="--label-filter=feature:jobset" make test-e2e-extended

# 只运行特定集成测试目录
INTEGRATION_TARGET='test/integration/singlecluster/scheduler' make test-integration
```

完整的标签分类和更多示例，请参阅[运行测试子集](#running-subset-of-integration-or-e2e-tests)。

type: docs
---

## 运行单元测试 {#running-unit-tests}

运行所有单元测试：
```shell
make test
```

运行 Webhook 的单元测试：
```shell
go test ./pkg/webhooks
```
[参考文档](https://pkg.go.dev/cmd/go#hdr-Testing_flags)运行与 `TestValidateClusterQueue` 正则表达式匹配的测试：
```shell
go test ./pkg/webhooks -run TestValidateClusterQueue
```

### 使用竞态检测运行单元测试 {#running-unit-tests-with-race-detection}

使用 `-race` 启用 Go 内置的竞态检测器：
```shell
go test ./pkg/scheduler/preemption/ -race
```

### 使用压力测试运行单元测试 {#running-unit-tests-with-stress}

循环运行单元测试并收集失败情况：
```shell
# 安装 go stress
go install golang.org/x/tools/cmd/stress@latest
# 编译测试（可以添加 -race 进行竞态检测）
go test ./pkg/scheduler/preemption/ -c
# 循环运行并报告失败
stress ./preemption.test -test.run TestPreemption
```

## 运行集成测试 {#running-integration-tests}

```shell
make test-integration
```

关于运行测试子集，请参阅[运行测试子集](#running-subset-of-integration-or-e2e-tests)。

## 运行 e2e 测试 {#running-e2e-tests}

e2e 测试需要构建 Kueue 镜像并将其加载到本地 Kind 集群中。首先执行构建步骤：

```shell
make kind-image-build
```

在 Apple Silicon（arm64）上，需设置 `PLATFORM`：
```shell
PLATFORM=linux/arm64 make kind-image-build
```

然后运行所需的测试目标：
```shell
make test-e2e-baseline
make test-e2e-extended
make test-e2e-sequential-baseline
make test-e2e-sequential-extended
make test-e2e-certmanager
make test-e2e-kueueviz
make test-tas-e2e-baseline
make test-tas-e2e-extended
make test-multikueue-e2e-baseline
make test-multikueue-e2e-extended
make test-multikueue-e2e-sequential
```

你可以通过设置 `E2E_K8S_FULL_VERSION` 变量来指定用于运行 e2e 测试的 Kubernetes 版本：
```shell
E2E_K8S_FULL_VERSION=1.35.0 make test-e2e-baseline
```

关于运行测试子集，请参阅 [运行测试子集](#running-subset-of-integration-or-e2e-tests)。

### DEV 模式（保留集群）{#dev-mode}

使用 `E2E_MODE=dev` 来创建或复用 kind 集群、重新构建/重新部署 Kueue、运行测试，并在测试结束后保留集群以便快速重跑或人工排查：

```shell
# 若不存在则创建；若已存在则复用。构建镜像、运行测试，并保留集群。
E2E_MODE=dev make kind-image-build test-e2e-baseline

# MultiKueue 的 dev 模式
E2E_MODE=dev make kind-image-build test-multikueue-e2e-baseline

# 循环运行（直到失败），同时保留集群
E2E_MODE=dev GINKGO_ARGS="--until-it-fails" make kind-image-build test-e2e-baseline

# 跳过重新安装 kueue（仅在 dev 模式下有效）
E2E_MODE=dev E2E_SKIP_REINSTALL=true make kind-image-build test-e2e-baseline
E2E_MODE=dev E2E_SKIP_REINSTALL=true make kind-image-build test-multikueue-e2e-baseline

# 跳过重新拉取依赖镜像并将其重新导入 kind（仅 dev 模式）
E2E_MODE=dev E2E_SKIP_IMAGE_RELOAD=true make kind-image-build test-e2e-baseline
```

{{% alert title="Note" color="primary" %}}
当在 `E2E_MODE=dev` 下复用保留的集群时，外部算子（MPI、KubeRay 等）只会安装一次。
如需在每次运行时强制重新安装它们，请设置 `E2E_ENFORCE_OPERATOR_UPDATE=true`。

将 `E2E_SKIP_IMAGE_RELOAD` 设置为真值（例如 `true`），可跳过对本地 Docker 缓存中已有的依赖镜像执行 `docker pull`，
也可跳过将镜像加载到节点 containerd 存储中已有该镜像引用的 kind worker 节点。
这在多节点集群上可以加快重复运行的速度。

Kueue 控制器镜像始终会重新加载到集群中，除非设置了 `E2E_SKIP_REINSTALL=true`，
因为你可能在同一标签下使用 `make kind-image-build` 重新构建了它。
{{% /alert %}}

测试结束后如需删除保留的集群：
- 常规 e2e 测试，请运行：
    ```shell
    kind delete clusters kind
    ```
- MultiKueue 测试，请运行：
    ```shell
    kind delete clusters kind kind-manager kind-worker1 kind-worker2
    ```

### 使用已发布或预发布镜像 {#using-a-released-or-staging-image}

若希望使用**已发布**或**预发布（staging）**的 Kueue 镜像而非从源码构建（无需执行 `kind-image-build`），可传入 `IMAGE_TAG`：

```shell
# 已发布版本
E2E_MODE=dev IMAGE_TAG=registry.k8s.io/kueue/kueue:v0.16.0 make test-e2e-baseline
E2E_MODE=dev IMAGE_TAG=registry.k8s.io/kueue/kueue:v0.16.0 make test-multikueue-e2e-baseline

# 预发布镜像（例如来自 PR 或每日构建）
E2E_MODE=dev IMAGE_TAG=us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:main make test-e2e-baseline
```

**使用与 manifest 匹配的已发布版本：** e2e 框架从仓库的 config 部署 CRD 等资源，仅通过 `IMAGE_TAG` 覆盖控制器镜像。若要对某一发布版本运行 e2e 且使用与该镜像匹配的 manifest：

1. 检出该版本的 tag（例如 `git checkout v0.16.0`）。仓库中的 CRD 与部署配置在每个版本中均已提交，无需执行 `make manifests`。
2. 使用相同镜像 tag 运行上述命令，例如 `E2E_MODE=dev IMAGE_TAG=registry.k8s.io/kueue/kueue:v0.16.0 make test-e2e-baseline`。

适用于在特定已发布版本上复现问题（例如值班排查）。若要在真实集群（非 e2e）中安装已发布版本，请参阅[安装已发布版本](/zh-cn/docs/installation/#install-a-released-version)。

## 运行集成或 e2e 测试子集 {#running-subset-of-integration-or-e2e-tests}

### 使用标签过滤集成测试 {#use-label-filters-for-integration-tests}

集成测试按控制器、作业类型、特性和区域打标签，可通过 `INTEGRATION_FILTERS` 和 `--label-filter` 运行特定子集：

**标签分类：**
- 控制器：`controller:workload`、`controller:localqueue`、`controller:clusterqueue`、`controller:admissioncheck`、`controller:resourceflavor`、`controller:provisioning`
- 作业类型：`job:batch`、`job:pod`、`job:jobset`、`job:pytorch`、`job:tensorflow`、`job:mpi`、`job:paddle`、`job:xgboost`、`job:jax`、`job:train`、`job:ray`、`job:appwrapper`、`job:sparkapplication`
- 特性：`feature:tas`、`feature:multikueue`、`feature:provisioning`、`feature:fairsharing`、`feature:admissionfairsharing`
- 区域：`area:core`、`area:jobs`、`area:admissionchecks`、`area:multikueue`

**示例：**
```shell
# 只运行 LocalQueue 测试
INTEGRATION_FILTERS="--label-filter=controller:localqueue" make test-integration

# 运行所有作业测试
INTEGRATION_FILTERS="--label-filter=area:jobs" make test-integration

# 运行 PyTorch 作业测试
INTEGRATION_FILTERS="--label-filter=job:pytorch" make test-integration

# 运行除 slow 以外的所有测试
INTEGRATION_FILTERS="--label-filter=!slow" make test-integration

# 运行核心测试（排除 slow）
INTEGRATION_FILTERS="--label-filter=area:core && !slow" make test-integration

# 运行 TAS 相关测试
INTEGRATION_FILTERS="--label-filter=feature:tas" make test-integration

# 运行 FairSharing 测试
INTEGRATION_FILTERS="--label-filter=feature:fairsharing" make test-integration
```

### 使用标签过滤 e2e 单集群测试 {#use-label-filters-for-e2e-singlecluster-tests}

单集群测试按特性和区域打标签，可通过 `GINKGO_ARGS` 和 `--label-filter` 运行特定测试：

**标签分类：**
- 特性：`appwrapper,certs,deployment,job,fairsharing,jaxjob,jobset,kuberay,kueuectl,leaderworkerset,metrics,pod,pytorchjob,statefulset,tas,trainjob,visibility,e2e_v1beta1,ha`

**示例：**
```shell
# 只运行 appwrapper 测试
GINKGO_ARGS="--label-filter=feature:appwrapper" make test-e2e-extended

# 使用 helm 只运行 deployment 测试
GINKGO_ARGS="--label-filter=feature:deployment" make test-e2e-baseline-helm

# 使用 helm 只运行 jobset 和 trainjob 测试
GINKGO_ARGS="--label-filter=feature:jobset,feature:trainjob" make test-e2e-extended
```

### 使用标签过滤 e2e 顺序测试 {#use-label-filters-for-e2e-sequential-tests}

顺序测试（Baseline 和 Extended）按特性打标签，可通过 `GINKGO_ARGS` 和 `--label-filter` 运行特定测试：

**标签分类（Baseline）：**
- 特性：`admissionfairsharing, certs, failurerecoverypolicy, localqueuemetrics, objectretentionpolicies, podintegrationautoenablement, reconcile, visibility, waitforpodsready`

**标签分类（Extended）：**
- 特性：`managejobswithoutqueuename, spark`

**示例：**
```shell
# 只运行 admissionfairsharing 测试（Baseline）
GINKGO_ARGS="--label-filter=feature:admissionfairsharing" make test-e2e-sequential-baseline

# 只运行 spark 测试（Extended）
GINKGO_ARGS="--label-filter=feature:spark" make test-e2e-sequential-extended
```

### 使用 Ginkgo --focus 参数 {#use-ginkgo-focus-arg}
```shell
GINKGO_ARGS="--focus=Scheduler" make test-integration
GINKGO_ARGS="--focus='Creating a Pod requesting TAS'" make test-e2e-baseline
```

### 使用 ginkgo.FIt {#use-ginkgo-fit}
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
make test-tas-e2e-baseline
make test-tas-e2e-extended
```
来测试特定的 TAS e2e 测试。

### 使用 INTEGRATION_TARGET {#use-integration-target}
```shell
INTEGRATION_TARGET='test/integration/singlecluster/scheduler' make test-integration
```

## 不稳定的集成/e2e 测试 {#flaky-integration-e2e-tests}
你可以使用 --until-it-fails 或 --repeat=N 参数来让 Ginkgo 重复运行测试，例如：
```shell
GINKGO_ARGS="--until-it-fails" make test-integration
GINKGO_ARGS="--repeat=10" make test-e2e-baseline
```
更多信息请参阅[这里](https://onsi.github.io/ginkgo/#repeating-spec-runs-and-managing-flaky-specs)

### 添加压力 {#adding-stress}
你可以运行 [stress](https://github.com/resurrecting-open-source-projects/stress) 工具来在测试期间增加 CPU 负载。例如，如果你在基于 Debian 的 Linux 上：
```shell
# 安装 stress：
sudo apt install stress
# 与测试一起运行 stress
/usr/bin/stress --cpu 80
```

### 分析日志 {#analyzing-logs}
Kueue 作为常规 pod 在 worker 节点上运行，在 e2e 测试中有 2 个副本在运行。Kueue 日志位于 `kind-worker/pods/kueue-system_kueue-controller-manager*/manager` 和 `kind-worker2/pods/kueue-system_kueue-controller-manager*/manager` 文件夹中。

对于每条日志消息，你可以看到消息来自哪个文件和行：
```log
2025-02-03T15:51:51.502425029Z stderr F 2025-02-03T15:51:51.502117824Z	LEVEL(-2)	cluster-queue-reconciler	core/clusterqueue_controller.go:341	ClusterQueue update event	{"clusterQueue": {"name":"cluster-queue"}}
```
这里，它是 `core/clusterqueue_controller.go:341`。

type: docs
---

## 进阶 {#advanced}

### 运行预提交验证测试 {#running-presubmission-verification-tests}
```shell
make verify
```

### 增加日志详细程度 {#increase-logging-verbosity}
`TEST_LOG_LEVEL` 统一控制所有测试目标的日志级别：

- `go test`、`make test`（单元测试）
- `make test-integration`（集成测试）
- `make test-*-e2e`（端到端测试）

使用更小的负数获取更详细的日志，使用更大的正数降低日志量。例如：
```shell
TEST_LOG_LEVEL=-5 make test-integration   # 更详细
TEST_LOG_LEVEL=-1 make test               # 比默认更少
```
默认值为 `TEST_LOG_LEVEL=-3`。

### 在 VSCode 中调试测试 {#debug-tests-in-vscode}
可以在 VSCode 中调试单元测试和集成测试。
你需要安装 [Go 扩展](https://marketplace.visualstudio.com/items?itemName=golang.Go)。
现在你将在如下行上方看到 `run test | debug test` 文本按钮：
```go
func TestValidateClusterQueue(t *testing.T) {
```
你可以点击 `debug test` 来调试特定测试。

对于集成测试，需要额外的步骤。在 settings.json 中，你需要在 `go.testEnvVars` 内添加两个变量：
- 运行 `ENVTEST_K8S_VERSION=1.35 make envtest && ./bin/setup-envtest use $ENVTEST_K8S_VERSION -p path` 并将路径分配给 `KUBEBUILDER_ASSETS` 变量
- 将 `KUEUE_BIN` 设置为你的 Kueue 仓库克隆目录内的 `bin` 目录
```json
"go.testEnvVars": {
    "KUBEBUILDER_ASSETS": "<上面输出的路径>",
    "KUEUE_BIN": "<你的-kueue-文件夹路径>/bin",
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
然后你可以使用 Ginkgo Test Explorer 的 GUI 来运行单个测试，前提是你已经启动了 kind 集群（有关说明，请参阅[旧方式：交互式附加模式](#legacy-interactive-attach-mode)）。

### 使用 Prometheus 调试指标 {#debugging-metrics-with-prometheus}

使用预配置了 Prometheus 的 Kind 集群进行指标调试：

```shell
E2E_MODE=dev GINKGO_ARGS="--label-filter=feature:prometheus" make kind-image-build test-e2e-baseline
```

更多详情，请参阅 [Setup Dev Monitoring](/zh-cn/docs/tasks/dev/setup_dev_monitoring)。

### 另请参阅 {#see-also}
- [Kubernetes 测试指南](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/testing.md)
- [Kubernetes 中的集成测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/integration-tests.md)
- [Kubernetes 中的端到端测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/e2e-tests.md)
- [Kubernetes 中的不稳定测试](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/flaky-tests.md)
