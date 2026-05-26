# KEP-NNNN: Automatic GOMEMLIMIT Tuning for Kueue Controller

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Container with tight memory limits](#story-1-container-with-tight-memory-limits)
    - [Story 2: Disable auto memlimit](#story-2-disable-auto-memlimit)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API](#configuration-api)
  - [Implementation](#implementation)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP proposes adding automatic `GOMEMLIMIT` tuning to the Kueue controller
manager. When running inside a container with a memory limit (set via cgroup),
the Go runtime does not automatically respect that limit. This can lead to
OOMKill events when the Go garbage collector is unaware of the container's
memory boundary.

By introducing a configurable `memlimitRatio` field under a new `runtime`
section in the Kueue configuration API, operators can instruct the Kueue
controller to automatically set `GOMEMLIMIT` to a fraction of the container's
memory limit. This is detected at startup via cgroup (with a fallback to total
system memory), using the well-established
[`github.com/KimMachineGun/automemlimit`](https://github.com/KimMachineGun/automemlimit)
library.

## Motivation

The Kueue controller manager runs as a Go process inside a Kubernetes Pod with
a container memory limit. The Go runtime's garbage collector is soft-limited by
`GOMEMLIMIT`, which defaults to no limit. When `GOMEMLIMIT` is not set, the GC
may allow heap growth beyond the container's cgroup memory limit, resulting in
the kernel OOM-killing the process.

This is a well-known issue for Go applications running in containers. While
Go 1.19+ introduced `GOMEMLIMIT`, it must be explicitly set. Many operators
either forget to set it or set it incorrectly.

This KEP proposes making this tuning automatic and configurable, reducing
operational burden and improving reliability.

### Goals

- Provide a configuration option (`runtime.memlimitRatio`) to automatically
  set `GOMEMLIMIT` based on the container's cgroup memory limit.
- Allow operators to tune the ratio (0–1) to leave headroom for non-Go
  overhead (e.g., cgroup metadata, kernel buffers).
- Disable the feature by default (ratio = 0 or unset) to preserve backward
  compatibility.
- Gracefully fall back to system memory when cgroup limits are not detectable
  (e.g., running outside a container).

### Non-Goals

- Dynamic adjustment of `GOMEMLIMIT` at runtime (only set once at startup).
- Automatic tuning of other Go runtime parameters (e.g., `GOMAXPROCS`,
  `GOGC`).
- Modifying the Kueue Deployment manifest to set `GOMEMLIMIT` directly.
- Providing a feature gate — the feature is opt-in via configuration and
  disabled by default.

## Proposal

Add a new optional `runtime` section to the Kueue `Configuration` API
(`v1beta2`), containing a single field:

- `memlimitRatio` — a `resource.Quantity` representing the fraction of the
  container's memory limit to use as `GOMEMLIMIT`.

At startup (after loading configuration but before starting controllers),
if `runtime.memlimitRatio` is set and greater than 0, the controller manager
calls `memlimit.SetGoMemLimitWithOpts` from the `automemlimit` library to
configure the Go runtime.

The library detects the memory limit via:
1. **cgroup v2** (`/sys/fs/cgroup/memory.max`)
2. **cgroup v1** (`/sys/fs/cgroup/memory/memory.limit_in_bytes`)
3. **Fallback**: total system memory

### User Stories

#### Story 1: Container with tight memory limits

An operator deploys Kueue with a container memory limit of 512Mi. They set
`runtime.memlimitRatio: "0.9"` in the Kueue configuration. At startup, Kueue
detects the 512Mi cgroup limit and sets `GOMEMLIMIT` to ~461Mi (90% of 512Mi).
The Go GC proactively reclaims memory before reaching the container boundary,
preventing OOMKill.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
runtime:
  memlimitRatio: "0.9"
```

#### Story 2: Disable auto memlimit

An operator manages `GOMEMLIMIT` manually via the container's environment
variable. They leave `runtime.memlimitRatio` unset (or set to `"0"`). Kueue
skips the auto-tuning entirely, preserving existing behavior.

### Notes/Constraints/Caveats

- The `memlimitRatio` is clamped to the range [0, 1]. Values outside this
  range are silently clamped.
- The feature is only applied at process startup. Changes to the container's
  memory limit after startup are not reflected.
- When running outside a container (no cgroup limit), the library falls back
  to total system memory, which may be larger than desired. Operators in
  this scenario should set `GOMEMLIMIT` manually or leave the ratio at 0.
- The `memlimitRatio` field uses `resource.Quantity` type for consistency
  with other Kueue configuration fields and to support decimal values like
  `0.9`.

### Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Setting `GOMEMLIMIT` too low causes excessive GC pressure and degraded performance | Operators can tune the ratio (e.g., 0.9 leaves 10% headroom). The default is 0 (disabled). |
| Incorrect cgroup detection on unusual container runtimes | The `automemlimit` library has broad support for cgroup v1/v2 and falls back to system memory. |
| New dependency on `github.com/KimMachineGun/automemlimit` | The library is vendored, lightweight, well-maintained, and used by other Kubernetes projects. |
| Breaking change to existing deployments | The feature is opt-in and disabled by default. No behavioral change unless explicitly configured. |

## Design Details

### Configuration API

A new `RuntimeConfig` struct is added to `apis/config/v1beta2/configuration_types.go`:

```go
// RuntimeConfig holds Go runtime tuning options for the kueue controller process.
type RuntimeConfig struct {
    // MemlimitRatio sets GOMEMLIMIT to the given ratio of the container's memory
    // limit (detected via cgroup, with a fallback to total system memory).
    // Valid values are in the range [0, 1]:
    //   - 0 (or unset) disables automatic memory limit tuning.
    //   - 1 uses the full container memory limit as GOMEMLIMIT.
    //   - A value between 0 and 1 uses that fraction (e.g., 0.9 = 90%).
    // +optional
    MemlimitRatio *apiresource.Quantity `json:"memlimitRatio,omitempty"`
}
```

Referenced from the top-level `Configuration`:

```go
type Configuration struct {
    // ...
    // Runtime configures Go runtime settings for the kueue controller.
    // +optional
    Runtime *RuntimeConfig `json:"runtime,omitempty"`
    // ...
}
```

### Implementation

The wiring in `cmd/kueue/main.go` is straightforward:

```go
if cfg.Runtime != nil && cfg.Runtime.MemlimitRatio != nil {
    goruntime.SetMemLimit(setupLog, cfg.Runtime.MemlimitRatio.AsApproximateFloat64())
}
```

The `SetMemLimit` function in `internal/goruntime/memory.go`:

```go
func SetMemLimit(logger logr.Logger, memlimitRatio float64) {
    // Clamp to [0, 1]
    if memlimitRatio >= 1.0 {
        memlimitRatio = 1.0
    } else if memlimitRatio <= 0.0 {
        return // disabled
    }

    if _, err := memlimit.SetGoMemLimitWithOpts(
        memlimit.WithRatio(memlimitRatio),
        memlimit.WithProvider(
            memlimit.ApplyFallback(
                memlimit.FromCgroup,
                memlimit.FromSystem,
            ),
        ),
    ); err != nil {
        logger.Error(err, "Failed to set GOMEMLIMIT automatically")
    }

    logger.Info("Set GOMEMLIMIT", "value", debug.SetMemoryLimit(-1))
}
```

Dependencies added (vendored):
- `github.com/KimMachineGun/automemlimit` — cgroup-aware `GOMEMLIMIT` setter
- `github.com/pbnjay/memory` — transitive dependency for system memory detection

### Test Plan

#### Unit tests

- `internal/goruntime/memory_test.go`:
  - `memlimitRatio = 0` → no call to `SetGoMemLimitWithOpts`
  - `memlimitRatio = 0.9` → ratio is passed correctly
  - `memlimitRatio = 1.5` → clamped to 1.0
  - `memlimitRatio = -0.1` → treated as 0 (disabled)
  - Error from `SetGoMemLimitWithOpts` is logged, not fatal

#### Integration tests

- Verify that when `runtime.memlimitRatio` is set in the configuration,
  `debug.SetMemoryLimit(-1)` returns a non-default value after startup.

#### e2e tests

- Not required for Alpha. The feature is a startup-time tuning knob with
  no observable behavioral change beyond GC behavior, which is difficult
  to assert in e2e tests.

### Graduation Criteria

**Alpha:**
- Configuration API (`runtime.memlimitRatio`) added to `v1beta2`.
- Startup-time `GOMEMLIMIT` tuning implemented and vendored.
- Unit tests for `SetMemLimit` and edge cases.
- Disabled by default.

**Beta:**
- Collect feedback from Alpha users.
- Integration test verifying `GOMEMLIMIT` is set after startup.
- Documentation in the Kueue configuration reference.

**Stable:**
- Multiple releases of successful Beta usage.
- Consider whether to enable by default with a conservative ratio (e.g., 0.9).

## Implementation History

- 2026-05-26: Initial KEP created.
- 2026-05-26: Prototype implementation in PR (branch `add-automemlimit`).

## Drawbacks

- Adds two new vendored dependencies (`automemlimit`, `memory`).
- The feature is only useful in containerized environments. On bare-metal
  without cgroup limits, the fallback to system memory may not be desirable.
- `GOMEMLIMIT` is set once at startup; if the container's memory limit
  changes dynamically (rare), the Go runtime is not updated.

## Alternatives

### 1. Set `GOMEMLIMIT` via environment variable in the Deployment manifest

Operators can set `GOMEMLIMIT=460MiB` directly in the Kueue Deployment's
container spec. This avoids code changes but requires manual calculation
and maintenance whenever the memory limit changes.

**Why not:** Shifts the burden to operators and is error-prone when memory
limits are adjusted.

### 2. Use Go's `runtime/debug.SetMemoryLimit` directly with a hardcoded ratio

Hardcode a ratio (e.g., 0.9) without exposing configuration.

**Why not:** Removes operator control. Some deployments may need different
ratios or want the feature disabled entirely.

### 3. Add a `GOMEMLIMIT` field instead of a ratio

Accept an absolute value (e.g., `runtime.goMemLimit: "460Mi"`) instead of
a ratio.

**Why not:** An absolute value must be updated whenever the container memory
limit changes. A ratio automatically adapts to the container's limit.
