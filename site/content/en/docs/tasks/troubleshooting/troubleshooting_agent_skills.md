---
title: "Troubleshooting with Agent Skills"
date: 2026-04-29
weight: 6
description: >
  Using Kueue's experimental agent skills for workload lineage and preemption investigations
---

Kueue includes experimental agent skills that help an AI agent follow repeatable troubleshooting
runbooks for common workload investigations. The skills live in
[`cmd/experimental/agent/skills`](https://github.com/kubernetes-sigs/kueue/tree/main/cmd/experimental/agent/skills)
and are intended for agents that can read repository instructions such as `AGENTS.md`.

{{% alert title="Experimental" color="warning" %}}
Agent skills and `AGENTS.md` support are experimental. They are not Kueue APIs, CLI commands,
or released binaries, and they do not provide backwards or forwards compatibility guarantees.
Agent behavior is non-deterministic, so a human must supervise the investigation and validate
the results before taking action.
{{% /alert %}}

## Before you begin

Before using these skills, make sure:

- You add an `@AGENTS.md` reference to your agent's configuration file.
- The agent can understand `AGENTS.md` and the files under `cmd/experimental/agent/skills`.
- You have a local copy of the Kueue repository that includes the skills.
- `kubectl` is configured for the cluster you want to inspect.
- Your Kubernetes RBAC permissions allow reading the Workloads, parent jobs, Pods, events,
  and namespaces needed for the investigation.

RBAC affects the result. For example, an agent might be able to trace resources in one namespace
but not resolve a preemptor Workload that lives in another namespace.

## Available skills

| Skill | Use it when | Typical input | Typical output |
| --- | --- | --- | --- |
| `kueue-lineage` | You need to trace ownership across Workload, Job, JobSet, Pod, Ray, Kubeflow, LeaderWorkerSet, Deployment, StatefulSet, or another supported job layer. | A resource kind, name, and namespace at any level of the ownership chain. | A tree from the Kueue Workload down to intermediate resources and Pods, with the starting resource marked when possible. |
| `kueue-who-preempted` | You need to understand why a Workload was evicted or preempted, and which Workload or parent job caused it. | The victim Workload name and namespace. | The preemptor Workload, parent job, preemption reason, and preemptor/preemptee ClusterQueue paths when RBAC allows resolving them. |

## Example prompts

To trace a resource lineage, ask the agent for the full Kueue lineage and provide the resource
you are starting from:

```text
Trace the Kueue lineage for Pod my-namespace/my-pod.
```

```text
Trace the Kueue lineage for Job my-namespace/my-job and show the related Pods.
```

To investigate preemption, provide the victim Workload:

```text
Find what preempted Workload my-namespace/job-my-job-19797.
```

```text
Why was Workload team-b/job-job-b-victim-54490 evicted?
```

The agent should use the skill runbook to choose the right `kubectl` queries, parse the
resulting Workload status or events, and report the relevant resources back to you.

## RBAC behavior

The `kueue-lineage` skill requires permission to read the resources in the lineage. Depending on
the starting point, this can include Workloads, Pods, Jobs, JobSets, Ray resources, Kubeflow
training jobs, Deployments, ReplicaSets, StatefulSets, or other supported job types in the same
namespace.

The `kueue-who-preempted` skill has two lookup paths:

- If you can list Workloads across all namespaces, the skill can search for the preemptor by
  `kueue.x-k8s.io/job-uid` or by Workload UID.
- If you only have namespace-scoped access, the skill can search the namespaces visible to you.
  When the preemptor lives in a namespace you cannot access, the skill should explain that an
  administrator needs to run the investigation with broader permissions.

## Source of truth

The repository [`AGENTS.md`](https://github.com/kubernetes-sigs/kueue/blob/main/AGENTS.md)
directs compatible agents to the skills index. The detailed runbooks are maintained with the
skills:

- [`cmd/experimental/agent/skills/README.md`](https://github.com/kubernetes-sigs/kueue/blob/main/cmd/experimental/agent/skills/README.md)
- [`cmd/experimental/agent/skills/kueue-lineage.md`](https://github.com/kubernetes-sigs/kueue/blob/main/cmd/experimental/agent/skills/kueue-lineage.md)
- [`cmd/experimental/agent/skills/kueue-who-preempted.md`](https://github.com/kubernetes-sigs/kueue/blob/main/cmd/experimental/agent/skills/kueue-who-preempted.md)
