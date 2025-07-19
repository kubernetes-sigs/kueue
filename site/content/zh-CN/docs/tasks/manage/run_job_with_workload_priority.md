---
title: "使用工作负载优先级（`WorkloadPriority`）运行作业（Job）"
date: 2023-10-02
weight: 4
description: >
  使用工作负载优先级运行作业（Job），此优先级（Priority）与 Pod 优先级无关
---

通常，在 Kueue 中，工作负载的优先级是使用 Pod 的优先级来计算的，用于排队和抢占。

通过使用 [`WorkloadPriorityClass`](/zh-CN/docs/concepts/workload_priority_class)，
你可以独立管理工作负载的优先级，以便进行排队和抢占，与 Pod 的优先级分开。

此页面包含如何使用工作负载优先级运行作业的说明。

## 在你开始之前

确保满足以下条件：

- 一个正在运行的 Kubernetes 集群。
- kubectl 命令行工具能够与你的集群通信。
- [Kueue 已安装](/zh-CN/docs/installation)。

## 0. 创建 WorkloadPriorityClass

首先应该创建 WorkloadPriorityClass。

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: WorkloadPriorityClass
metadata:
  name: sample-priority
value: 10000
description: "Sample priority"
```

## 1. 创建带有 `kueue.x-k8s.io/priority-class` 标签的作业

你可以通过设置 `kueue.x-k8s.io/priority-class` 标签来指定 `WorkloadPriorityClass`。
这与其他 CRD（例如 `RayJob`）相同。

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: sample-job
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
  parallelism: 3
  completions: 3
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: registry.k8s.io/e2e-test-images/agnhost:latest
        args: ["pause"]
      restartPolicy: Never
```

Kueue 为上述作业生成了以下 `Workload`。
工作负载的优先级在 Kueue 中用于排队、抢占和其他调度过程。
此优先级不影响 Pod 的优先级。

工作负载的 `Priority` 字段总是可变的，因为这对于抢占可能有用。
工作负载的 `PriorityClassSource` 和 `PriorityClassName` 字段是不可变的。

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: job-sample-job-7f173
spec:
  priorityClassSource: kueue.x-k8s.io/workloadpriorityclass
  priorityClassName: sample-priority
  priority: 10000
  queueName: user-queue
  podSets:
  - count: 3
    name: dummy-job
    template:
      spec:
        containers:
        - name: dummy-job
          image: registry.k8s.io/e2e-test-images/agnhost:latest
          args: ["pause"]
```
