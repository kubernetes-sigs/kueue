---
name: ctx-structured-logging
description: Review that context.Context is not silenced — use ctrl.LoggerFrom(ctx) for structured logging.
---

# Skill: Don't Silence `ctx` — Use It for Structured Logging

**Flag:** A method that accepts `context.Context` but silences it with `_ = ctx`.

**Ask:** Use `ctrl.LoggerFrom(ctx)` to obtain a logger and emit structured log lines at V4–V5 verbosity for internal state transitions (e.g., which ClusterQueue is being reconciled, what was cleared). Silencing ctx means the method can't be traced and loses the request-scoped logger.

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
