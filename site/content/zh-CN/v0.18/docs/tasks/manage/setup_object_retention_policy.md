---
title: "设置 Workload 的垃圾回收"
date: 2025-05-16
weight: 11
description: >
  通过定义保留策略来配置自动垃圾回收已完成或已停用的 Workload。
---

本指南演示如何在 Kueue 中启用并配置可选的对象保留策略，以在指定时间后自动删除已完成或已停用的
Workload。默认情况下，Kueue 会在集群中永久保留所有 Workload 对象；通过对象保留策略，可以释放
etcd 存储并降低 Kueue 的内存占用。


## 前置条件 {#prerequisites}

- 可正常运行的 Kueue **v0.12** 或更高版本。

## 设置保留策略 {#set-up-a-retention-policy}

按照此处描述的说明[安装自定义配置的发布版本](/zh-CN/docs/installation#install-a-custom-configured-released-version)，
并通过添加以下字段扩展配置：

```yaml
      objectRetentionPolicies:
        workloads:
          afterFinished: "1h"
          afterDeactivatedByKueue: "1h"
```

### Workload 保留策略 {#workload-retention-policy}

Workload 的保留策略在 `.objectRetentionPolicies.workloads` 字段下定义。
包含以下可选字段：
- `afterFinished`：已完成Workload在多长时间后被删除。
- `afterDeactivatedByKueue`：Kueue 已停用的 Workload（例如 Job、JobSet 或其他自定义
	Workload 类型）在多长时间后被删除。

## 示例 {#example}

### Kueue 配置 {##kueue-configuration}

**配置** Kueue 使用 1 分钟的保留策略，并启用 [waitForPodsReady](/zh-CN/docs/tasks/manage/setup_wait_for_pods_ready)：

```yaml
  objectRetentionPolicies:
    workloads:
      afterFinished: "1m"
      afterDeactivatedByKueue: "1m"
  waitForPodsReady:
    enable: true
    timeout: 2m
    recoveryTimeout: 1m
    blockAdmission: false
    requeuingStrategy:
      backoffLimitCount: 0
```

---

### 场景 A：成功完成的 Workload {#scenario-a-successfully-finished-workloa}

1. **提交** 一个应能正常完成的简单 Workload：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: successful-cq
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: [cpu]
    flavors:
    - name: objectretention-rf
      resources:
      - name: cpu
        nominalQuota: "2"
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: default
  name: successful-lq
spec:
  clusterQueue: successful-cq
---
apiVersion: batch/v1
kind: Job
metadata:
  name: successful-job
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: successful-lq
spec:
  parallelism: 1
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: registry.k8s.io/e2e-test-images/agnhost:2.53
          args: ["entrypoint-tester"]
          resources:
            requests:
              cpu: "1"
```

2. 观察状态转为 `Finished`。大约在 1 分钟后，Kueue 会自动删除该 Workload：

```bash
# ~1m after Finished
kubectl get workloads.kueue.x-k8s.io -n default
# <successful-workload not found>
   
kubectl get jobs -n default
# NAME             STATUS     COMPLETIONS   DURATION   AGE
# successful-job   Complete   1/1                      1m
```

---

### 场景 B：通过 `waitForPodsReady` 驱逐 Workload {#scenario-b-evicted-workload-via-waitforpodsready}

1. **配置** Kueue 使得 [Deployment](/zh-CN/docs/installation#install-a-custom-configured-released-version)
    可使用超过节点容量的资源：

```yaml
        resources:
          limits:
            cpu: "100" # 或任何大于节点容量的值
```

2. **提交** 一个请求量超过节点可用资源的 Workload：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: limited-cq
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: objectretention-rf
      resources:
      - name: cpu
        nominalQuota: "100" # 超过节点可用量
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: default
  name: limited-lq
spec:
  clusterQueue: limited-cq
---
apiVersion: batch/v1
kind: Job
metadata:
  name: limited-job
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: limited-lq
spec:
  parallelism: 1
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: registry.k8s.io/e2e-test-images/agnhost:2.53
          args: ["entrypoint-tester"]
          resources:
            requests:
              cpu: "100" # 超过节点可用量
```

3. 在 Pod 未全部就绪的情况下，Kueue 会驱逐并停用该 Workload：

```bash
# ~2m after submission
kubectl get workloads.kueue.x-k8s.io -n default
# NAME                    QUEUE    RESERVED IN   ADMITTED   FINISHED   AGE
# limited-workload                               False                 2m
```

4. 在驱逐后约 1 分钟，已停用的 Workload 会被垃圾回收：

```bash
# ~1m after eviction
kubectl get workloads.kueue.x-k8s.io -n default
# <limited-workload not found>
   
kubectl get jobs -n default
# <limited-job not found>
```

## 注意事项 {#notes}

- `afterDeactivatedByKueue` 表示在 Kueue 将 Workload（例如 Job、JobSet 或其他自定义 workload 类型）标记为已停用后，
  等待多长时间再自动删除该 Workload。删除已停用的 Workload 可能会级联删除并非由 Kueue 创建的对象，
  因为删除父级 Workload 的 owner 引用（例如 JobSet）可能触发对从属资源的垃圾回收。如果你是通过手动或设置 
  `.spec.active=false` 的方式停用 Workload，则 `afterDeactivatedByKueue` 不会生效。
- 如果保留的时长配置错误（无效的 duration 字符串），控制器将无法启动。
- 删除是在 reconciler 循环中同步处理的；如果集群有数千个过期的 Workload，
  集群首次启动时可能需要一些时间才能删除这些 Workload。
``` 
