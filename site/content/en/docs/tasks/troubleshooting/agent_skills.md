---
title: "Agent Skills"
date: 2026-04-27
weight: 6
description: >
  Troubleshooting Kueue workloads with agent skills
---

Kueue includes experimental agent skills under
`cmd/experimental/agent/skills/`. These skills are prompt guides that help an
agent choose the right `kubectl` lookups when investigating common Kueue
workload questions.

Use the skills as troubleshooting aids. They do not replace Kubernetes RBAC, and
they only return information that the user or agent can access through the
current kubeconfig.

## Trace Workload Lineage

Use `kueue-lineage` when you need to map a Kueue Workload to its parent job and
pods, or when the user starts from a pod, Job, JobSet, or another supported job
type and wants the full resource tree.

The skill follows `ownerReferences` and the `kueue.x-k8s.io/job-uid` label to
find the Workload, then uses job-type-specific labels to list child jobs and
pods. This is useful for answering questions such as:

* Which pods belong to this Workload?
* Which Workload owns this Job or pod?
* What child Jobs were created for this JobSet-backed Workload?

## Identify Who Preempted a Workload

Use `kueue-who-preempted` when a Workload was evicted or preempted and you need
to identify the preemptor.

The skill starts from the victim Workload's `Preempted` event or `Evicted`
condition, extracts the preemptor UID and Job UID, and then searches for the
matching Workload. If the preemptor is in another namespace, the result depends
on whether the current user can list Workloads across namespaces.

If the user lacks all-namespace access and the preemptor cannot be found in
accessible namespaces, ask a cluster administrator to run the lookup with
cluster-scoped Workload list permissions.

## Running the Skills

Read the skill Markdown file that matches the investigation:

```bash
cat cmd/experimental/agent/skills/kueue-lineage.md
cat cmd/experimental/agent/skills/kueue-who-preempted.md
```

Then follow its steps with the user's namespace, resource name, and kubeconfig.
Prefer read-only `kubectl get`, `kubectl describe`, and `kubectl auth can-i`
commands while investigating.
