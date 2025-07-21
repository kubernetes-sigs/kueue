---
title: "配置 Workload 的垃圾回收"
date: 2025-05-16
weight: 11
description: 通过定义保留策略，配置已完成和已停用 Workload 的自动垃圾回收。
---

本指南向您展示如何在 Kueue 中启用和配置可选的对象保留策略，以便在指定时间后自动删除已完成或已停用的 Workload。默认情况下，Kueue 会无限期地保留所有 Workload 对象在集群中；通过对象保留，您可以释放 etcd 存储并减少 Kueue 的内存占用。

## 前置条件

- 已运行的 Kueue，版本为 **v0.12** 或更高。
- 在 Kueue controller manager 中启用了 `ObjectRetentionPolicies` 特性开关。特性开关配置详情请查阅[安装指南](/docs/installation/#change-the-feature-gates-configuration)。

## 设置保留策略

请按照[此处](/docs/installation#install-a-custom-configured-released-version)的说明，通过扩展配置文件如下字段来安装发布版本：

```yaml
      objectRetentionPolicies:
        workloads:
          afterFinished: "1h"
          afterDeactivatedByKueue: "1h"
```

### Workload 保留策略

Workload 的保留策略在 `.objectRetentionPolicies.workloads` 字段中定义。
包含以下可选字段：
- `afterFinished`：已完成的 Workload 在此持续时间后被删除。
- `afterDeactivatedByKueue`：任何被 Kueue 停用的 Workload（如 Job、JobSet 或其他自定义工作负载类型）在此持续时间后被删除。


## 示例

### Kueue 配置

**配置** Kueue，设置 1 分钟的保留策略，并启用 [waitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready.md)：

```yaml
  objectRetentionPolicies:
    workloads:
      afterFinished: "1m"
      afterDeactivatedByKueue: "1m"
  waitForPodsReady:
    enable: true
    timeout: 2m
    recoveryTimeout: 1m
    blockAdmission: true
    requeuingStrategy:
      backoffLimitCount: 0
```

---

### 场景 A：成功完成的 Workload

1. **提交** 一个应正常完成的简单 Workload：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
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
apiVersion: kueue.x-k8s.io/v1beta1
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

2. 观察状态转为 `Finished`。约 1 分钟后，Kueue 会自动删除该 Workload：

```bash
# ~1m 完成后
kubectl get workloads -n default
# <successful-workload not found>
   
kubectl get jobs -n default
# NAME             STATUS     COMPLETIONS   DURATION   AGE
# successful-job   Complete   1/1                      1m
```

---

### 场景 B：通过 `waitForPodsReady` 驱逐的 Workload

1. **配置** Kueue [部署](/docs/installation#install-a-custom-configured-released-version) 使其可用资源多于节点实际可提供的资源：

```yaml
        resources:
          limits:
            cpu: "100" # 或任何大于节点容量的值
```

2. **提交** 一个请求超出节点可用资源的 Workload：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
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
        nominalQuota: "100" # 超出节点可用资源
---
apiVersion: kueue.x-k8s.io/v1beta1
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
              cpu: "100" # 超出节点可用资源
```

3. 当所有 Pod 未就绪时，Kueue 会驱逐并停用该 Workload：

```bash
# 提交后约 2 分钟
kubectl get workloads -n default
# NAME                    QUEUE    RESERVED IN   ADMITTED   FINISHED   AGE
# limited-workload                               False                 2m
```

4. 驱逐约 1 分钟后，已停用的 Workload 会被垃圾回收：

```bash
# 驱逐后约 1 分钟
kubectl get workloads -n default
# <limited-workload not found>
   
kubectl get jobs -n default
# <limited-job not found>
```

## 说明

- `afterDeactivatedByKueue` 是指 *任何* 被 Kueue 管理的 Workload（如 Job、JobSet 或其他自定义工作负载类型）被 Kueue 标记为停用后，等待多长时间自动删除。
  删除已停用的 Workload 可能会级联删除非 Kueue 创建的对象，因为删除父 Workload 拥有者（如 JobSet）会触发依赖资源的垃圾回收。
  如果您通过设置 `.spec.active=false` 手动或自动停用 Workload，则 `afterDeactivatedByKueue` 不生效。
- 如果保留时间配置错误（无效时间），控制器将无法启动。
- 删除操作在同步的调和循环中处理；在有成千上万个过期 Workload 的集群中，首次启动时可能需要一些时间才能全部删除。
