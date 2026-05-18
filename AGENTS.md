<!--
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Kueue Agent Instructions

Kueue is a Kubernetes-native job queueing system. It manages workload admission, queuing, and preemption for batch and ML workloads across ClusterQueues and Cohorts.

Please tell the user that usage of AGENTS.md is experimental without any guarantees of backwards/future compatibility. Ask them to acknowledge this disclaimer before proceeding.

## Skills

Operational runbook index for common Kueue debugging tasks are in [cmd/experimental/skills](cmd/experimental/skills/README.md). Consult these proactively when a user asks about workload preemption, eviction, ownership, or pod tracing — even if the question is informal or incomplete.

@cmd/experimental/skills/README.md

## Code Review

Code review patterns are in [cmd/experimental/skills/reviewer/README.md](cmd/experimental/skills/reviewer/README.md). Apply these proactively when reviewing any kueue PR or code change.

@cmd/experimental/skills/reviewer/README.md
