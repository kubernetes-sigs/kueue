---
title: "Configure all-or-nothing scheduling with ready Pods"
date: 2022-03-14
weight: 5
description: All-or-nothing scheduling implementation based on timeout
---

Some jobs require all Pods to run simultaneously to work;
for example, synchronous distributed training or MPI-based jobs require inter-Pod communication.
Under the default Kueue configuration, if the available physical resources don't match the quotas configured in Kueue, such jobs may deadlock.
However, if the Pods of these jobs can be scheduled sequentially, they can complete successfully.

To address this need, starting from version 0.3.0, we introduced an optional mechanism,
configured through the `waitForPodsReady` flag, providing a simple implementation of all-or-nothing scheduling.
When enabled, Kueue monitors workloads until all their Pods are ready (i.e., scheduled, running, and passing optional readiness checks).
If all Pods corresponding to a workload are not fully ready within the configured timeout period,
the workload will be evicted and requeued.

This page shows you how to configure Kueue to use `waitForPodsReady`,
which is a simple implementation of all-or-nothing scheduling.
This page is for [batch administrators](/docs/tasks#batch-administrator).

## Before you begin {#before-you-begin}

Make sure you meet the following requirements:

- Kubernetes cluster is running.
- kubectl command-line tool can communicate with your cluster.
- [Kueue is installed](/docs/installation) with version 0.3.0 or higher.

## Enabling waitForPodsReady {#enabling-waitforpodsready}

Follow the instructions described [here](/docs/installation#install-a-custom-configured-released-version)
to install a release version by extending the configuration with the following fields:

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

{{% alert title="Note" color="primary" %}}
If you update an existing Kueue installation, you may need to restart the `kueue-controller-manager` Pod
for Kueue to read the updated configuration. In this case, run:

```shell
kubectl delete pods --all -n kueue-system
```
{{% /alert %}}

`timeout` (`waitForPodsReady.timeout`) is an optional parameter with a default value of 5 minutes.

When an admitted Workload exceeds the `timeout`,
and its Pods are not all scheduled (i.e., the Workload condition remains `PodsReady=False`),
the Workload's admission will be cancelled, the corresponding job will be suspended, and the Workload will be requeued.

`recoveryTimeout` is an optional parameter,
used for Workloads that are running but have one or more Pods in an unready state (such as Pod failures).
If Pods are not ready, Workloads usually cannot progress,
leading to resource waste. To prevent this, users can configure a timeout period they are willing to wait for Pod recovery.
If `recoveryTimeout` expires, similar to regular timeout, the Workload will be evicted and requeued.
This parameter has no default value and must be explicitly set.

`blockAdmission` (`waitForPodsReady.blockAdmission`) is an optional parameter.
When enabled, Workloads will be admitted sequentially to prevent deadlock situations as shown in the example below.

### Requeuing Strategy {#requeuing-strategy}

{{< feature-state state="stable" for_version="v0.6" >}}

{{% alert title="Note" color="primary" %}}
`backoffBaseSeconds` and `backoffMaxSeconds` are only available in Kueue v0.7.0 and higher
{{% /alert %}}

`requeuingStrategy` (`waitForPodsReady.requeuingStrategy`) contains the following optional parameters:
- `timestamp`
- `backoffLimitCount`
- `backoffBaseSeconds`
- `backoffMaxSeconds`

The `timestamp` field defines the timestamp that Kueue uses to sort Workloads in the queue:

- `Eviction` (default): The `lastTransitionTime` of the `Evicted=true` condition with reason `PodsReadyTimeout` in the Workload.
- `Creation`: The creationTimestamp of the Workload.

If you want Workloads evicted by `PodsReadyTimeout` to return to their original position in the queue when requeued,
set timestamp to `Creation`.

Kueue will requeue Workloads evicted by `PodsReadyTimeout`,
until the requeue count reaches `backoffLimitCount`.
If `backoffLimitCount` is not specified, Workloads will be requeued continuously and indefinitely based on `timestamp`.
Once the requeue count reaches the limit, Kueue will [deactivate the Workload](/docs/concepts/workload/#active).

After each timeout, the time for Workload requeuing will increase exponentially with base 2.
The first delay is determined by the `backoffBaseSeconds` parameter (default is 60).
The maximum backoff time can be configured by setting `backoffMaxSeconds` (default is 3600).
With default values, evicted Workloads will be requeued after approximately `60, 120, 240, ..., 3600, ..., 3600` seconds.
Even when the backoff time reaches `backoffMaxSeconds`,
Kueue will continue requeuing at `backoffMaxSeconds` intervals,
until the requeue count reaches `backoffLimitCount`.

## Examples {#examples}

This example demonstrates the impact of enabling `waitForPodsReady` in Kueue.
We create two jobs,
both requiring all their Pods to run simultaneously to complete.
The cluster resources are only sufficient to run one of the jobs at a time.

{{% alert title="Note" color="primary" %}}
This example uses a cluster with auto-scaling disabled,
to simulate a situation where resources cannot meet the configured quotas.
{{% /alert %}}

### 1. Preparation {#1-preparation}

First, check the total amount of allocatable memory in the cluster.
This can usually be done with the following command:

```shell
TOTAL_ALLOCATABLE=$(kubectl get node --selector='!node-role.kubernetes.io/master,!node-role.kubernetes.io/control-plane' -o jsonpath='{range .items[*]}{.status.allocatable.memory}{"\n"}{end}' | numfmt --from=auto | awk '{s+=$1} END {print s}')
echo $TOTAL_ALLOCATABLE
```

In our example, the output is `8838569984`, which can be approximated as `8429Mi`.

#### Configure ClusterQueue Quota

We configure the memory flavor to be twice the cluster's allocatable memory,
to simulate a resource shortage situation.

Save the following cluster queues configuration as `cluster-queues.yaml`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-flavor"
---
apiVersion: kueue.x-k8s.io/v1beta1
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
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue"
spec:
  clusterQueue: "cluster-queue"
```

Then apply the configuration:

```shell
kubectl apply -f cluster-queues.yaml
```

#### Prepare Job Template

Save the following job template as `job-template.yaml` file.
Note the `_ID_` placeholder, which will be used to generate configurations for the two jobs.
Configure the container's memory field to be 75% of the allocatable memory per Pod.
In this example, it's `75%*(8429Mi/20)=316Mi`.
In this case, resources are insufficient to run all Pods of both jobs simultaneously, making deadlock likely.

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

#### Additional Quick Job

We also prepared an additional job to increase timing differences, making deadlock more likely to occur.
Save the following yaml as `quick-job.yaml`:

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

### 2. Induce Deadlock Under Default Configuration (Optional) {#2-induce-a-deadlock-under-the-default-configuration-optional}

#### Run the Jobs {#run-the-jobs}

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
