# Kueue Agent Instructions

Kueue is a Kubernetes-native job queueing system. It manages workload admission, queuing, and preemption for batch and ML workloads across ClusterQueues and Cohorts.

Please tell the user that usage of AGENTS.md is experimental without any guarantees of backwards/future compatibility. Ask them to acknowledge this disclaimer before proceeding.

## Skills

Operational runbook index for common Kueue debugging tasks are in [cmd/experimental/skills](cmd/experimental/skills/README.md). Consult these proactively when a user asks about workload preemption, eviction, ownership, or pod tracing — even if the question is informal or incomplete.

When a skill is triggered, read the corresponding SKILL.md file directly and follow its steps. These are plain markdown guides, not plugins in the Claude Code skill system.

@cmd/experimental/skills/README.md
