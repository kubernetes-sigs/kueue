# Comparing failed vs successful runs

Reference for step 7 of [SKILL.md](../SKILL.md). Use this only when escalating from
the main debugging flow — skip it for deterministic failures.

The same test in the success run is your reference: it tells you what *should* have
happened. Compare both runs along three dimensions.

## 6.1 Dimension 1: STEP timings

In Ginkgo build-log output, every `ginkgo.By(...)` produces a `STEP: <description> @ <timestamp>` line. List the STEPs of the failing test from both runs side by side. The step *just before* the failure, and its time delta to the previous step, is usually where the divergence lives.

```sh
perl -pe 's/\e\[[0-9;]*[a-zA-Z]//g' build-logs/failed/build-log.txt   | grep -E "STEP:" | grep "<test or namespace>" > /tmp/steps-failed.txt
perl -pe 's/\e\[[0-9;]*[a-zA-Z]//g' build-logs/success/build-log.txt  | grep -E "STEP:" | grep "<test or namespace>" > /tmp/steps-success.txt
diff /tmp/steps-failed.txt /tmp/steps-success.txt
```

## 6.2 Dimension 2: Controller events for the failing test's namespace

Filter both controller logs (or both build-logs, for integration tests) by the test namespace and the relevant object names. Look for differences in:
- Number of events
- Reasons / status values
- Time gaps (a 9-second silence on one side often means a wedged loop)
- Sequence of events (e.g., create → admit → delete vs. create → admit → ... still admitted)

## 6.3 Dimension 3: Resource lifecycle

Trace the specific objects (workloads, pods, LWS, etc.) named in the failure message through both runs. When was each created, admitted, updated, deleted? A failing run often shows an object that exists briefly (or not at all) where the successful run shows it persisting (or vice versa).

## 6.4 Produce a side-by-side findings table

One row per dimension; columns for `Failed run`, `Successful run`, `Notable difference`. Anchor every cell with a timestamp and a file path (link the build-log or pod log directly).

If the focused comparison turns up nothing useful, widen the scope: include BeforeSuite/BeforeAll logs, the entire scheduler cycle history, kubelet events. Do this only when the focused view isn't conclusive — the broad view is noisy.
