title: "使用 Python 运行 Job"
linkTitle: "Python"
date: 2023-07-05
weight: 7
description: 在启用了 Kueue 的环境里运行 Job
---

本指南适用于[批处理用户](/zh-cn/v0.19/docs/tasks#batch-user)他们具有基本的 Python 与 Kubernetes 交互经验。
更多信息，请参见 [Kueue 概述](/zh-cn/v0.19/docs/overview)。

## 开始之前 {#before-you-begin}

检查[管理集群配额](/zh-cn/v0.19/docs/tasks/manage/administer_cluster_quotas)
了解初始集群设置的详细信息。你还需要安装 [uv](https://docs.astral.sh/uv/)。
每个示例脚本包含[内联脚本元数据 (PEP 723)](https://peps.python.org/pep-0723/)，
因此 `uv run` 会自动创建隔离环境并安装所需的依赖项。

```bash
# 安装 uv（如果尚未安装）
curl -LsSf https://astral.sh/uv/install.sh | sh
```

无需手动 `pip install` 或虚拟环境设置 — 只需使用 `uv run` 运行脚本。

你可以按照 [Kueue 安装说明](https://github.com/kubernetes-sigs/kueue#installation)安装 Kueue，或者使用下面的安装示例。

## Kueue 在 Python 中 {#kueue-in-python}

Kueue 的核心是一个控制器，用于管理[自定义资源](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)。
因此，要使用 Python 与其交互，我们不需要一个专门的 SDK，而是可以使用 Kubernetes Python 库提供的通用函数。
在本指南中，我们提供了几个示例，用于以这种风格与 Kueue 交互。
如果你希望请求新的示例或需要帮助解决特定用例，请[提交 Issue](https://github.com/kubernetes-sigs/kueue/issues)。

## 示例 {#examples}

以下示例演示了使用 Python 与 Kueue 交互的不同用例。

### 安装 Kueue {#install-kueue}

此示例演示如何将 Kueue 安装到现有集群。
你可以将此脚本保存到本地机器，例如 `install-kueue-queues.py`。

{{< include file="v0.19/examples/python/install-kueue-queues.py" lang="python" >}}

然后运行如下：

```bash
uv run install-kueue-queues.py 
```

```console
⭐️ Installing Kueue...
⭐️ Applying queues from single-clusterqueue-setup.yaml...
```

你也可以指定版本：

```bash
uv run install-kueue-queues.py --version {{< param "version" >}}
```

### 示例作业 {#sample-job}

对于下一个示例，让我们从一个已安装 Kueue 的集群开始，首先创建我们的队列：

{{< include file="v0.19/examples/python/sample-job.py" code="true" lang="python" >}}

然后运行如下：

```bash
uv run sample-job.py
```
```console
📦️ Container image selected is registry.k8s.io/e2e-test-images/agnhost:2.53...
⭐️ Creating sample job with prefix sample-job-...
Use:
"kubectl get queue" to see queue assignment
"kubectl get jobs" to see jobs
```

或者尝试更改作业名称 (`generateName`)：

```bash
uv run sample-job.py --job-name sleep-job-
```

```console
📦️ Container image selected is registry.k8s.io/e2e-test-images/agnhost:2.53...
⭐️ Creating sample job with prefix sleep-job-...
Use:
"kubectl get queue" to see queue assignment
"kubectl get jobs" to see jobs
```

你也可以通过 `--image` 和 `--args` 更改容器镜像和参数。
你可以通过编辑示例脚本来配置更多定制化参数。

### 与队列和作业交互 {#interact-with-queues-and-jobs}

如果你正在开发一个提交作业并需要与之交互的应用程序，你可能希望直接与队列或作业交互。
在运行上述示例后，你可以测试以下示例以与结果交互。
将以下内容写入一个名为 `sample-queue-control.py` 的脚本。

{{< include file="v0.19/examples/python/sample-queue-control.py" lang="python" >}}

为了使输出更有趣，我们可以先运行几个随机作业：

```bash
uv run sample-job.py
uv run sample-job.py
uv run sample-job.py --job-name tacos
```

然后运行脚本来查看你之前提交的队列和作业。

```bash
uv run sample-queue-control.py
```
```console
⛑️  Local Queues
Found queue user-queue
  Admitted workloads: 3
  Pending workloads: 0
  Flavor default-flavor has resources [{'name': 'cpu', 'total': '3'}, {'name': 'memory', 'total': '600Mi'}]

💼️ Jobs
Found job sample-job-8n5sb
  Succeeded: 3
  Ready: 0
Found job sample-job-gnxtl
  Succeeded: 1
  Ready: 0
Found job tacos46bqw
  Succeeded: 1
  Ready: 1
```

如果你想按作业标签过滤作业，可以通过`job["metadata"]["labels"]["kueue.x-k8s.io/queue-name"]` 来实现。
要按名称列出特定作业，你可以执行：

```python
from kubernetes import client, config

# Interact with batch
config.load_kube_config()
batch_api = client.BatchV1Api()

# This is providing the name, and namespace
job = batch_api.read_namespaced_job("tacos46bqw", "default")
print(job)
```

请参见 [BatchV1](https://github.com/kubernetes-client/python/blob/master/kubernetes/docs/BatchV1Api.md)
API 文档了解更多调用。


### Flux Operator 作业 {#flux-operator-job}

对于此示例，我们将使用 [Flux Operator](https://github.com/flux-framework/flux-operator)
提交作业，并特别使用 [Python SDK](https://github.com/flux-framework/flux-operator/tree/main/sdk/python/v1alpha1)
来轻松完成此操作。鉴于我们在[设置](#开始之前)中创建的 Python 环境，
我们可以直接将其安装到其中，如下所示：

`fluxoperator` 依赖在脚本的内联元数据中声明，`uv run` 会自动安装。

我们还需要[安装 Flux operator](https://flux-framework.org/flux-operator/getting_started/user-guide.html#quick-install)。

```bash
kubectl apply -f https://raw.githubusercontent.com/flux-framework/flux-operator/main/examples/dist/flux-operator.yaml
```

将以下内容写入 `sample-flux-operator-job.py`：

{{< include file="v0.19/examples/python/sample-flux-operator-job.py" lang="python" >}}

现在尝试运行示例：

```bash
uv run sample-flux-operator-job.py
```
```console
📦️ Container image selected is ghcr.io/flux-framework/flux-restful-api...
⭐️ Creating sample job with prefix hello-world...
Use:
"kubectl get queue" to see queue assignment
"kubectl get pods" to see pods
```

你将能够几乎立即看到 MiniCluster 作业被本地队列接纳：

```bash
kubectl get queue
```
```console
NAME         CLUSTERQUEUE    PENDING WORKLOADS   ADMITTED WORKLOADS
user-queue   cluster-queue   0                   1
```

并且 4 个 pods 正在运行（我们创建了一个具有 4 个节点的联网集群）：

```bash
kubectl get pods
```
```console
NAME                       READY   STATUS      RESTARTS   AGE
hello-world7qgqd-0-wp596   1/1     Running     0          7s
hello-world7qgqd-1-d7r87   1/1     Running     0          7s
hello-world7qgqd-2-rfn4t   1/1     Running     0          7s
hello-world7qgqd-3-blvtn   1/1     Running     0          7s
```

如果你查看主 broker Pod 的日志（上述作业中的索引 0），你会看到很多
调试输出，并且你可以看到最后运行的是 "hello world"：

```bash
kubectl logs hello-world7qgqd-0-wp596 
```

<details>

<summary>Flux Operator Lead Broker 输出</summary>

```console
🌀 Submit Mode: flux start -o --config /etc/flux/config -Scron.directory=/etc/flux/system/cron.d   -Stbon.fanout=256   -Srundir=/run/flux    -Sstatedir=/var/lib/flux   -Slocal-uri=local:///run/flux/local     -Slog-stderr-level=6    -Slog-stderr-mode=local  flux submit  -n 1 --quiet  --watch echo hello world
broker.info[0]: start: none->join 0.399725ms
broker.info[0]: parent-none: join->init 0.030894ms
cron.info[0]: synchronizing cron tasks to event heartbeat.pulse
job-manager.info[0]: restart: 0 jobs
job-manager.info[0]: restart: 0 running jobs
job-manager.info[0]: restart: checkpoint.job-manager not found
broker.info[0]: rc1.0: running /etc/flux/rc1.d/01-sched-fluxion
sched-fluxion-resource.info[0]: version 0.27.0-15-gc90fbcc2
sched-fluxion-resource.warning[0]: create_reader: allowlist unsupported
sched-fluxion-resource.info[0]: populate_resource_db: loaded resources from core's resource.acquire
sched-fluxion-qmanager.info[0]: version 0.27.0-15-gc90fbcc2
broker.info[0]: rc1.0: running /etc/flux/rc1.d/02-cron
broker.info[0]: rc1.0: /etc/flux/rc1 Exited (rc=0) 0.5s
broker.info[0]: rc1-success: init->quorum 0.485239s
broker.info[0]: online: hello-world7qgqd-0 (ranks 0)
broker.info[0]: online: hello-world7qgqd-[0-3] (ranks 0-3)
broker.info[0]: quorum-full: quorum->run 0.354587s
hello world
broker.info[0]: rc2.0: flux submit -n 1 --quiet --watch echo hello world Exited (rc=0) 0.3s
broker.info[0]: rc2-success: run->cleanup 0.308392s
broker.info[0]: cleanup.0: flux queue stop --quiet --all --nocheckpoint Exited (rc=0) 0.1s
broker.info[0]: cleanup.1: flux cancel --user=all --quiet --states RUN Exited (rc=0) 0.1s
broker.info[0]: cleanup.2: flux queue idle --quiet Exited (rc=0) 0.1s
broker.info[0]: cleanup-success: cleanup->shutdown 0.252899s
broker.info[0]: children-complete: shutdown->finalize 47.6699ms
broker.info[0]: rc3.0: running /etc/flux/rc3.d/01-sched-fluxion
broker.info[0]: rc3.0: /etc/flux/rc3 Exited (rc=0) 0.2s
broker.info[0]: rc3-success: finalize->goodbye 0.212425s
broker.info[0]: goodbye: goodbye->exit 0.06917ms
```

</details>

如果你提交并请求四个任务，你将看到 "hello world" 四次：

```bash
uv run sample-flux-operator-job.py --tasks 4
```
```console
...
broker.info[0]: quorum-full: quorum->run 23.5812s
hello world
hello world
hello world
hello world
```

你可以进一步自定义作业，并可以在 [Flux Operator 问题板](https://github.com/flux-framework/flux-operator/issues)上提问。
最后，有关如何使用 YAML 在 Python 之外完成此操作的说明，请参见[运行 Flux MiniCluster](/zh-cn/v0.19/docs/tasks/run/external_workloads/flux_miniclusters/)。

