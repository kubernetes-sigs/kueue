---
title: "使用 Agent Skills 进行故障排除"
date: 2026-04-29
weight: 6
description: >
  使用 Kueue 的实验性 agent skills 排查 Workload 链路和抢占问题
---

Kueue 包含实验性的 agent skills，可帮助 AI agent 按照可重复的 runbook
排查常见的 Workload 问题。这些 skills 位于
[`cmd/experimental/agent/skills`](https://github.com/kubernetes-sigs/kueue/tree/main/cmd/experimental/agent/skills)，
面向能够读取 `AGENTS.md` 等仓库说明的 agent。

{{% alert title="实验性能力" color="warning" %}}
Agent skills 和 `AGENTS.md` 支持仍是实验性的。它们不是 Kueue API、CLI 命令或发布二进制，
也不提供向后或向前兼容性保证。
Agent 的行为具有非确定性，因此必须由人工监督排查过程，并在采取操作前验证结果。
{{% /alert %}}

## 开始之前 {#before-you-begin}

使用这些 skills 前，请确认：

- 已在你的 agent 配置文件中添加 `@AGENTS.md` 引用。
- Agent 能够理解 `AGENTS.md` 以及 `cmd/experimental/agent/skills` 下的文件。
- 你有一份包含这些 skills 的 Kueue 仓库本地副本。
- `kubectl` 已配置到要排查的集群。
- 你的 Kubernetes RBAC 权限允许读取排查所需的 Workload、父级 Job、Pod、事件和命名空间。

RBAC 会影响结果。例如，agent 可能能够追踪某个命名空间内的资源，但无法解析位于其他命名空间的
preemptor Workload。

## 可用 skills {#available-skills}

| Skill | 适用场景 | 典型输入 | 典型输出 |
| --- | --- | --- | --- |
| `kueue-lineage` | 需要在 Workload、Job、JobSet、Pod、Ray、Kubeflow、LeaderWorkerSet、Deployment、StatefulSet 或其他受支持作业层级之间追踪所有权关系。 | 所有权链上任意层级资源的 kind、name 和 namespace。 | 从 Kueue Workload 到中间资源和 Pod 的树形关系，并在可行时标记起始资源。 |
| `kueue-who-preempted` | 需要了解某个 Workload 为什么被驱逐或抢占，以及哪个 Workload 或父级 Job 触发了抢占。 | 被抢占的 Workload 名称和命名空间。 | 在 RBAC 允许时，输出 preemptor Workload、父级 Job、抢占原因，以及 preemptor/preemptee 的 ClusterQueue 路径。 |

## 示例提示词 {#example-prompts}

如果要追踪资源链路，请要求 agent 输出完整的 Kueue lineage，并提供起始资源：

```text
Trace the Kueue lineage for Pod my-namespace/my-pod.
```

```text
Trace the Kueue lineage for Job my-namespace/my-job and show the related Pods.
```

如果要排查抢占，请提供被抢占的 Workload：

```text
Find what preempted Workload my-namespace/job-my-job-19797.
```

```text
Why was Workload team-b/job-job-b-victim-54490 evicted?
```

Agent 应使用 skill runbook 选择合适的 `kubectl` 查询，解析 Workload 状态或事件，
并向你报告相关资源。

## RBAC 行为 {#rbac-behavior}

`kueue-lineage` skill 需要读取链路中资源的权限。根据起始资源不同，这可能包括同一命名空间中的
Workload、Pod、Job、JobSet、Ray 资源、Kubeflow training job、Deployment、ReplicaSet、
StatefulSet 或其他受支持的作业类型。

`kueue-who-preempted` skill 有两种查找路径：

- 如果你可以跨所有命名空间列出 Workload，skill 可以通过 `kueue.x-k8s.io/job-uid`
  或 Workload UID 搜索 preemptor。
- 如果你只有命名空间级访问权限，skill 可以搜索你可见的命名空间。当 preemptor 位于你无法访问的
  命名空间时，skill 应说明需要管理员使用更高权限运行排查。

## 事实来源 {#source-of-truth}

仓库中的 [`AGENTS.md`](https://github.com/kubernetes-sigs/kueue/blob/main/AGENTS.md)
会指示兼容的 agent 使用 skills 索引。详细 runbook 随 skills 一起维护：

- [`cmd/experimental/agent/skills/README.md`](https://github.com/kubernetes-sigs/kueue/blob/main/cmd/experimental/agent/skills/README.md)
- [`cmd/experimental/agent/skills/kueue-lineage.md`](https://github.com/kubernetes-sigs/kueue/blob/main/cmd/experimental/agent/skills/kueue-lineage.md)
- [`cmd/experimental/agent/skills/kueue-who-preempted.md`](https://github.com/kubernetes-sigs/kueue/blob/main/cmd/experimental/agent/skills/kueue-who-preempted.md)
