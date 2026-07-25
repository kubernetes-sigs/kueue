---
title: "使用就绪态 Pod 配置全有或全无调度"
date: 2022-03-14
weight: 5
description: 基于超时的全有或全无调度实现
---

有些作业需要所有 Pod 同时运行才能工作；
例如，同步的分布式训练或基于 MPI 的作业需要 Pod 间通信。
在默认的 Kueue 配置下，如果可用的物理资源与 Kueue 中配置的配额不匹配，这类作业可能会死锁。
而如果这些作业的 Pod 能够顺序调度，则可以顺利完成。

为了解决这一需求，从 0.3.0 版本开始，我们引入了一个可选机制，
通过 `waitForPodsReady` 标志进行配置，提供了全有或全无调度的简单实现。
启用后，Kueue 会监控工作负载，直到其所有 Pod 就绪（即已调度、运行并通过可选的就绪态检测）。
如果在配置的超时时间内工作负载对应的所有 Pod 并未完全就绪，
则该工作负载会被驱逐并重新排队。

本页面向你展示如何配置 Kueue 使用 `waitForPodsReady`，
这是全有或全无调度的简单实现。
本页面面向[批处理管理员](/zh-CN/docs/tasks#batch-administrator)。

## 开始之前 {#before-you-begin}

请确保满足以下条件：

- Kubernetes 集群已运行。
- kubectl 命令行工具已能与你的集群通信。
- [已安装 Kueue](/zh-CN/docs/installation)，版本为 0.3.0 或更高。

## 启用 waitForPodsReady {#enabling-waitforpodsready}

请按照[此处](/zh-CN/docs/installation#install-a-custom-configured-released-version)的说明，
使用如下字段扩展配置来安装某个发布版本：

```yaml
    waitForPodsReady:
      enable: true
      timeout: 10m
      recoveryTimeout: 3m
      blockAdmission: true
      requeuingStrategy:
        timestamp: Eviction | Creation
        backoffLimitCount: 5
        backoffBaseSeconds: 60
        backoffMaxSeconds: 3600
```

{{% alert title="注意" color="primary" %}}
如果你更新了现有的 Kueue 安装，可能需要重启 `kueue-controller-manager` Pod
以使 Kueue 读取更新后的配置。在这种情况下请运行：

```shell
kubectl delete pods --all -n kueue-system
```
{{% /alert %}}

`timeout`（`waitForPodsReady.timeout`）是一个可选参数，默认值为 5 分钟。

当已准入的 Workload 超过 `timeout`，
且其 Pod 还未全部调度（即 Workload 条件仍为 `PodsReady=False`），
则该 Workload 的准入会被取消，相应的作业会被挂起，Workload 会被重新入队。

`recoveryTimeout` 是一个可选参数，
用于已运行但有一个或多个 Pod 处于未就绪状态（如 Pod 故障）的 Workload。
如果 Pod 未就绪，Workload 通常无法推进，
导致资源浪费。为防止这种情况，用户可以配置一个愿意等待恢复 Pod 的超时时间。
如果 `recoveryTimeout` 到期，与常规超时类似，Workload 会被驱逐并重新入队。
该参数无默认值，需显式设置。

`blockAdmission`（`waitForPodsReady.blockAdmission`）是一个可选参数。
启用后，Workload 会被顺序准入，以防止如下例所示的死锁情况。

### 重新排队策略 {#requeuing-strategy}

{{< feature-state state="stable" for_version="v0.6" >}}

{{% alert title="注意" color="primary" %}}
`backoffBaseSeconds` 和 `backoffMaxSeconds` 仅在 Kueue v0.7.0 及更高版本可用
{{% /alert %}}

`requeuingStrategy`（`waitForPodsReady.requeuingStrategy`）包含以下可选参数：
- `timestamp`
- `backoffLimitCount`
- `backoffBaseSeconds`
- `backoffMaxSeconds`

`timestamp` 字段定义 Kueue 用于在队列中排序 Workload 的时间戳：

- `Eviction`（默认）：Workload 中 `Evicted=true` 条件且原因为 `PodsReadyTimeout` 的 `lastTransitionTime`。
- `Creation`：Workload 的 creationTimestamp。

如果希望被 `PodsReadyTimeout` 驱逐的 Workload 重新入队时回到队列原始位置，
应将 timestamp 设置为 `Creation`。

Kueue 会将因 `PodsReadyTimeout` 被驱逐的 Workload 重新入队，
直到重新入队次数达到 `backoffLimitCount`。
如果未指定 `backoffLimitCount`，Workload 会根据 `timestamp` 不断、无限制地重新入队。
一旦重新入队次数达到上限，Kueue 会[停用该 Workload](/zh-CN/docs/concepts/workload/#active)。

每次超时后，Workload 重新入队的时间会以 2 为底数指数递增。
第一次延迟由 `backoffBaseSeconds` 参数决定（默认为 60）。
可通过设置 `backoffMaxSeconds`（默认为 3600）配置最大退避时间。
使用默认值时，驱逐的 Workload 会在大约 `60, 120, 240, ..., 3600, ..., 3600` 秒后重新入队。
即使退避时间达到 `backoffMaxSeconds`，
Kueue 仍会以 `backoffMaxSeconds` 的间隔继续重新入队，
直到重新入队次数达到 `backoffLimitCount`。

## 示例 {#examples}

本示例演示在 Kueue 中启用 `waitForPodsReady` 的影响。
我们创建两个作业，
这两个作业都要求其所有 Pod 同时运行才能完成。
集群资源只够同时运行其中一个作业。

{{% alert title="注意" color="primary" %}}
本示例使用禁用自动扩容的集群，
以模拟资源无法满足配置配额的情况。
{{% /alert %}}

### 1. 准备工作 {#1-preparation}

首先，检查集群中可分配内存的总量。
通常可以用以下命令完成：

```shell
TOTAL_ALLOCATABLE=$(kubectl get node --selector='!node-role.kubernetes.io/master,!node-role.kubernetes.io/control-plane' -o jsonpath='{range .items[*]}{.status.allocatable.memory}{"\n"}{end}' | numfmt --from=auto | awk '{s+=$1} END {print s}')
echo $TOTAL_ALLOCATABLE
```

在我们的例子中，输出为 `8838569984`，可近似为 `8429Mi`。

#### 配置 ClusterQueue 配额

我们将 memory flavor 配置为集群可分配内存的两倍，
以模拟资源不足的情况。

将以下 cluster queues 配置保存为 `cluster-queues.yaml`：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: "default-flavor"
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 16858Mi # double the value of allocatable memory in the cluster
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue"
spec:
  clusterQueue: "cluster-queue"
```

然后应用配置：

```shell
kubectl apply -f cluster-queues.yaml
```

#### 准备作业模板

将以下作业模板保存为 `job-template.yaml` 文件。
注意 `_ID_` 占位符，将用于为两个作业生成配置。
请将容器的 memory 字段配置为每个 Pod 可分配内存的 75%。
在本例中为 `75%*(8429Mi/20)=316Mi`。
在这种情况下，资源不足以同时运行两个作业的所有 Pod，容易死锁。

```yaml
apiVersion: v1
kind: Service
metadata:
  name: svc_ID_
spec:
  clusterIP: None
  selector:
    job-name: job_ID_
  ports:
  - name: http
    protocol: TCP
    port: 8080
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: script-code_ID_
data:
  main.py: |
    from http.server import BaseHTTPRequestHandler, HTTPServer
    from urllib.request import urlopen
    import sys, os, time, logging

    logging.basicConfig(stream=sys.stdout, level=logging.DEBUG)
    serverPort = 8080
    INDEX_COUNT = int(sys.argv[1])
    index = int(os.environ.get('JOB_COMPLETION_INDEX'))
    logger = logging.getLogger('LOG' + str(index))

    class WorkerServer(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.end_headers()
            if "exit" in self.path:
              self.wfile.write(bytes("Exiting", "utf-8"))
              self.wfile.close()
              sys.exit(0)
            else:
              self.wfile.write(bytes("Running", "utf-8"))

    def call_until_success(url):
      while True:
        try:
          logger.info("Calling URL: " + url)
          with urlopen(url) as response:
            response_content = response.read().decode('utf-8')
            logger.info("Response content from %s: %s" % (url, response_content))
            return
        except Exception as e:
          logger.warning("Got exception when calling %s: %s" % (url, e))
        time.sleep(1)

    if __name__ == "__main__":
      if index == 0:
        for i in range(1, INDEX_COUNT):
          call_until_success("http://job_ID_-%d.svc_ID_:8080/ping" % i)
        logger.info("All workers running")

        time.sleep(10) # sleep 10s to simulate doing something

        for i in range(1, INDEX_COUNT):
          call_until_success("http://job_ID_-%d.svc_ID_:8080/exit" % i)
        logger.info("All workers stopped")
      else:
        webServer = HTTPServer(("", serverPort), WorkerServer)
        logger.info("Server started at port %s" % serverPort)
        webServer.serve_forever()
---

apiVersion: batch/v1
kind: Job
metadata:
  name: job_ID_
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 20
  completions: 20
  completionMode: Indexed
  suspend: true
  template:
    spec:
      subdomain: svc_ID_
      volumes:
      - name: script-volume
        configMap:
          name: script-code_ID_
      containers:
      - name: main
        image: python:bullseye
        command: ["python"]
        args:
        - /script-path/main.py
        - "20"
        ports:
        - containerPort: 8080
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            memory: "316Mi" # choose the value as 75% * (total allocatable memory / 20)
        volumeMounts:
          - mountPath: /script-path
            name: script-volume
      restartPolicy: Never
  backoffLimit: 0
```

#### 额外的快速作业

我们还准备了一个额外的作业以增加时序差异，使死锁更容易发生。
将以下 yaml 保存为 `quick-job.yaml`：

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: quick-job
  annotations:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 50
  completions: 50
  suspend: true
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: sleep
        image: bash:5
        command: ["bash"]
        args: ["-c", 'echo "Hello world"']
        resources:
          requests:
            memory: "1"
  backoffLimit: 0
```

### 2. 在默认配置下诱发死锁（可选） {#2-induce-a-deadlock-under-the-default-configuration-optional}

#### 运行作业 {#run-the-jobs}

```yaml
sed 's/_ID_/1/g' job-template.yaml > /tmp/job1.yaml
sed 's/_ID_/2/g' job-template.yaml > /tmp/job2.yaml
kubectl create -f quick-job.yaml
kubectl create -f /tmp/job1.yaml
kubectl create -f /tmp/job2.yaml
```

稍后通过以下命令检查 Pod 状态：

```shell
kubectl get pods
```

输出如下（省略 `quick-job` 的 Pod）：

```shell
NAME            READY   STATUS      RESTARTS   AGE
job1-0-9pvs8    1/1     Running     0          28m
job1-1-w9zht    1/1     Running     0          28m
job1-10-fg99v   1/1     Running     0          28m
job1-11-4gspm   1/1     Running     0          28m
job1-12-w5jft   1/1     Running     0          28m
job1-13-8d5jk   1/1     Running     0          28m
job1-14-h5q8x   1/1     Running     0          28m
job1-15-kkv4j   0/1     Pending     0          28m
job1-16-frs8k   0/1     Pending     0          28m
job1-17-g78g8   0/1     Pending     0          28m
job1-18-2ghmt   0/1     Pending     0          28m
job1-19-4w2j5   0/1     Pending     0          28m
job1-2-9s486    1/1     Running     0          28m
job1-3-s9kh4    1/1     Running     0          28m
job1-4-52mj9    1/1     Running     0          28m
job1-5-bpjv5    1/1     Running     0          28m
job1-6-7f7tj    1/1     Running     0          28m
job1-7-pnq7w    1/1     Running     0          28m
job1-8-7s894    1/1     Running     0          28m
job1-9-kz4gt    1/1     Running     0          28m
job2-0-x6xvg    1/1     Running     0          28m
job2-1-flkpj    1/1     Running     0          28m
job2-10-vf4j9   1/1     Running     0          28m
job2-11-ktbld   0/1     Pending     0          28m
job2-12-sf4xb   1/1     Running     0          28m
job2-13-9j7lp   0/1     Pending     0          28m
job2-14-czc6l   1/1     Running     0          28m
job2-15-m77zt   0/1     Pending     0          28m
job2-16-7p7fs   0/1     Pending     0          28m
job2-17-sfdmj   0/1     Pending     0          28m
job2-18-cs4lg   0/1     Pending     0          28m
job2-19-x66dt   0/1     Pending     0          28m
job2-2-hnqjv    1/1     Running     0          28m
job2-3-pkwhw    1/1     Running     0          28m
job2-4-gdtsh    1/1     Running     0          28m
job2-5-6swdc    1/1     Running     0          28m
job2-6-qb6sp    1/1     Running     0          28m
job2-7-grcg4    0/1     Pending     0          28m
job2-8-kg568    1/1     Running     0          28m
job2-9-hvwj8    0/1     Pending     0          28m
```

这些作业现在已死锁，无法继续。

#### 清理

通过以下命令清理作业：

```shell
kubectl delete -f quick-job.yaml
kubectl delete -f /tmp/job1.yaml
kubectl delete -f /tmp/job2.yaml
```

### 3. 启用 waitForPodsReady 后运行 {#3-run-with-waitforpodsready-enabled}

#### 启用 waitForPodsReady {#enable-waitforpodsready}

按照[此处](#enabling-waitforpodsready)的说明更新 Kueue 配置。

#### 运行作业 {#run-the-jobs}

运行 `start.sh` 脚本

```yaml
sed 's/_ID_/1/g' job-template.yaml > /tmp/job1.yaml
sed 's/_ID_/2/g' job-template.yaml > /tmp/job2.yaml
kubectl create -f quick-job.yaml
kubectl create -f /tmp/job1.yaml
kubectl create -f /tmp/job2.yaml
```

#### 监控进度

每隔几秒执行以下命令以监控进度：

```shell
kubectl get pods
```

省略已完成 `quick` 作业的 Pod。

`job1` 启动时的输出，注意 `job2` 仍处于挂起状态：

```shell
NAME            READY   STATUS              RESTARTS   AGE
job1-0-gc284    0/1     ContainerCreating   0          1s
job1-1-xz555    0/1     ContainerCreating   0          1s
job1-10-2ltws   0/1     Pending             0          1s
job1-11-r4778   0/1     ContainerCreating   0          1s
job1-12-xx8mn   0/1     Pending             0          1s
job1-13-glb8j   0/1     Pending             0          1s
job1-14-gnjpg   0/1     Pending             0          1s
job1-15-dzlqh   0/1     Pending             0          1s
job1-16-ljnj9   0/1     Pending             0          1s
job1-17-78tzv   0/1     Pending             0          1s
job1-18-4lhw2   0/1     Pending             0          1s
job1-19-hx6zv   0/1     Pending             0          1s
job1-2-hqlc6    0/1     ContainerCreating   0          1s
job1-3-zx55w    0/1     ContainerCreating   0          1s
job1-4-k2tb4    0/1     Pending             0          1s
job1-5-2zcw2    0/1     ContainerCreating   0          1s
job1-6-m2qzw    0/1     ContainerCreating   0          1s
job1-7-hgp9n    0/1     ContainerCreating   0          1s
job1-8-ss248    0/1     ContainerCreating   0          1s
job1-9-nwqmj    0/1     ContainerCreating   0          1s
```

当 `job1` 正在运行时，`job2` 被解冻，
因为 `job` 已经分配好了所有需要的资源，此时的输出如下：

```shell
NAME            READY   STATUS      RESTARTS   AGE
job1-0-gc284    1/1     Running     0          9s
job1-1-xz555    1/1     Running     0          9s
job1-10-2ltws   1/1     Running     0          9s
job1-11-r4778   1/1     Running     0          9s
job1-12-xx8mn   1/1     Running     0          9s
job1-13-glb8j   1/1     Running     0          9s
job1-14-gnjpg   1/1     Running     0          9s
job1-15-dzlqh   1/1     Running     0          9s
job1-16-ljnj9   1/1     Running     0          9s
job1-17-78tzv   1/1     Running     0          9s
job1-18-4lhw2   1/1     Running     0          9s
job1-19-hx6zv   1/1     Running     0          9s
job1-2-hqlc6    1/1     Running     0          9s
job1-3-zx55w    1/1     Running     0          9s
job1-4-k2tb4    1/1     Running     0          9s
job1-5-2zcw2    1/1     Running     0          9s
job1-6-m2qzw    1/1     Running     0          9s
job1-7-hgp9n    1/1     Running     0          9s
job1-8-ss248    1/1     Running     0          9s
job1-9-nwqmj    1/1     Running     0          9s
job2-0-djnjd    1/1     Running     0          3s
job2-1-trw7b    0/1     Pending     0          2s
job2-10-228cc   0/1     Pending     0          2s
job2-11-2ct8m   0/1     Pending     0          2s
job2-12-sxkqm   0/1     Pending     0          2s
job2-13-md92n   0/1     Pending     0          2s
job2-14-4v2ww   0/1     Pending     0          2s
job2-15-sph8h   0/1     Pending     0          2s
job2-16-2nvk2   0/1     Pending     0          2s
job2-17-f7g6z   0/1     Pending     0          2s
job2-18-9t9xd   0/1     Pending     0          2s
job2-19-tgf5c   0/1     Pending     0          2s
job2-2-9hcsd    0/1     Pending     0          2s
job2-3-557lt    0/1     Pending     0          2s
job2-4-k2d6b    0/1     Pending     0          2s
job2-5-nkkhx    0/1     Pending     0          2s
job2-6-5r76n    0/1     Pending     0          2s
job2-7-pmzb5    0/1     Pending     0          2s
job2-8-xdqtp    0/1     Pending     0          2s
job2-9-c4rcl    0/1     Pending     0          2s
```

一旦 `job1` 完成，就会释放 `job2` 运行其 Pod 所需的资源，最终所有作业都能完成。

#### 清理

通过以下命令清理作业：

```shell
kubectl delete -f quick-job.yaml
kubectl delete -f /tmp/job1.yaml
kubectl delete -f /tmp/job2.yaml
```

## 局限性 {#drawbacks}

如果开启了 `waitForPodsReady`，
即使集群资源足够可以同时启动多个 Workload，
它们的调度也可能会因为“排队顺序”而被不必要地延迟。
