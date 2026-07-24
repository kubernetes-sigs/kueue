# CI resource right-sizing for Kueue prow jobs

Tooling to measure the real CPU/memory usage of Kueue's prow CI jobs and derive
right-sizing recommendations for their Kubernetes `requests`/`limits`. It pulls per-build
time series from the prow Prometheus backend, renders diagnostic plots, and produces a
recommendation table.

---

## Quick start

The scripts read from and write to a **work directory** (`--out-dir`, default the current
dir); they never write into the repo. Run them by path from a scratch dir.

```bash
# absolute path to this directory
STATS=$(git rev-parse --show-toplevel)/hack/infra/stats

# one command: fetch -> render all images -> aggregate the recommendation table
"$STATS"/collect_stats.sh \
  --job-regex '^pull-.*-main' --days 30 --step 30s --min-duration 10s \
  --out-dir ./ci-stats --concurrency 2 --sleep 2 --retries 2
```

Or run the three stages yourself:

```bash
# 1. fetch every matching job into ./ci-stats/<job>_30d_30s/raw_series.json
"$STATS"/fetch_prow_metrics.py --job-regex '^pull-.*-main' --days 30 --step 30s \
    --min-duration 10s --out-dir ./ci-stats

# 2. render the 5 images + recommendation.json for one job folder
"$STATS"/plot.py ./ci-stats/pull-kueue-test-e2e-baseline-main-1-36_30d_30s

# 3. fold every job's recommendation.json into ./ci-stats/recommendation.md
"$STATS"/aggregate_reco.py ./ci-stats
```

Requires Python 3 with `numpy`, `scipy`, and `matplotlib`. `fetch_prow_metrics.py` needs
outbound access to the anonymous prow Prometheus proxy (no token); `plot.py` and
`aggregate_reco.py` are offline. Preview a regex without downloading with
`--list-jobs`.

---

## Output layout

Everything lands under the work directory:

```
<out-dir>/
├── fetch.log                       # append-only run log
├── failed_jobs.txt                 # only when a batch has leftovers
├── recommendation.md               # aggregate table across all jobs (aggregate_reco.py)
└── <job>_<range>_<step>/           # one folder per job, e.g. ..._30d_30s
    ├── raw_series.json             # merged per-build time series (fetch)
    ├── per_build_summary.csv       # one row per build (fetch)
    ├── aggregate_stats.json        # per-metric distribution across builds (fetch)
    ├── recommendation.json         # structured current-vs-recommended sizing + burstiness (plot.py)
    ├── dist_mean.png
    ├── dist_peak.png
    ├── dist_crest.png
    ├── dist_throttle.png
    ├── timeline_throttle.png
    └── timeline_network.png
```

Re-running `fetch_prow_metrics.py` **resumes**: complete jobs are skipped and partial
folders are cleaned and refetched, so only the gaps are downloaded.

---

## Metrics collected

Pulled per build (the `test` container) over the requested range/step. Cancelled builds
and builds shorter than `--min-duration` (e.g. 10s, to drop compile-error runs) are
excluded.

| metric (Prometheus) | meaning |
|---|---|
| `prow:job:cpu_usage_seconds_rate:1m` | CPU core usage |
| `prow:job:memory_working_set_bytes` | RAM usage |
| `prow:job:resource_requests_cpu_cores` | configured k8s CPU request |
| `prow:job:resource_requests_memory_bytes` | configured k8s memory request |
| `prow:job:resource_limits_cpu_cores` | configured k8s CPU limit |
| `prow:job:resource_limits_memory_bytes` | configured k8s memory limit |
| `container_pressure_cpu_waiting_seconds_total` | seconds **≥1** thread waited for CPU (PSI "some"); high ⇒ CPU-hungry |
| `container_pressure_cpu_stalled_seconds_total` | seconds **all** threads waited for CPU (PSI "full"); stalled ≤ waiting |
| `container_network_receive_bytes_total` | cumulative bytes received (network **in**); pod-scoped, `eth0` only, attributed per build via the job's prow:job pods |
| `container_network_transmit_bytes_total` | cumulative bytes transmitted (network **out**); pod-scoped, `eth0` only, attributed per build via the job's prow:job pods |

The two network metrics are fetched alongside cpu/mem — there is no flag to disable them. Unlike the
other metrics (pre-aggregated `prow:job:*` recording rules), network comes from raw cAdvisor counters
that must be scanned and joined per pod at query time — many heavy queries per job that stress the
shared prow proxy and can make wide `--job-regex` batch pulls unstable (403/502/504). Network is
therefore **best-effort**: it retries harder per request (8 vs 5 elsewhere), and if the queries still
fail the job simply writes its cpu/mem data with no `net_*` series (and `plot.py` skips the
`timeline_network.png` plot for that job).

CPU "cores" = CPU-seconds of work per wall-second (a rate). Build nodes have ~7 usable
cores, so a 7-core request packs one build per node; cutting it toward 3–4 lets two share
a node.

---

## How the CPU request is sized (pluggable)

`recommendation.json` carries a **single** recommended CPU request, produced by one of the
named algorithms in `CPU_RECOMMENDERS` (in `plot.py`), selected with `--cpu-algorithm`.
Adding an algorithm is plug-and-play: register a `(data, min_dur, cfg) -> (value, stats)`
function in that dict and it becomes selectable with no other wiring. The chosen algorithm's
name and supporting percentiles are written under `cpu.algorithm` and `cpu.stats`.

| `--cpu-algorithm` | how it sizes the request |
|---|---|
| `target-duration` (default) | Work-conserving: assumes CPU work (avg × duration) is invariant to the request, so it sizes to the value that would stretch each build to about `--cpu-target-min` minutes — p95 of the per-build target mean CPU (see `dist_mean_new_cpu.png`), plus optional `--cpu-legroom-frac`, rounded up to `--cpu-resolution`. |
| `p95-mean` | Conservative (the original approach): p95 of the per-build mean CPU × 1.15, rounded up to whole cores. Ignores build duration, so it never trades runtime for cores. |

Both cap the recommendation at the current limit (test-infra forces `request == limit`) and
exclude OOM-killed builds. The recommended CPU limit is set equal to the recommended CPU
request (`request == limit`, Guaranteed QoS).

## The diagrams (and how each is drawn)

`plot.py` writes five PNGs per job.

### `dist_mean.png` — the per-build mean CPU distribution
For each build, compute the **average** CPU (and memory) usage across its samples; that
gives one number per build. Histogram those numbers across all builds. Overlays: the
p50/p95/p99 across builds, the current k8s request/limit (purple), and the recommended
request/limit (gold, the chosen algorithm's value). The bulk of the mean distribution is
the sustained demand the request should cover.

### `dist_peak.png` — sizes memory
Same as above but reduces each build to its **peak** usage. Memory is sized off these
peaks — recommended memory = **largest per-build peak × 1.15**, taken over healthy builds
only (builds that OOM-killed are excluded first, since their peak sits pinned at the
ceiling and would otherwise dictate the value). CPU peak reads inflated (a Prometheus
`rate()` extrapolation artifact over sparse samples), so it is *not* used to size CPU.

### `dist_crest.png` — is the job's CPU usage stable or bursty?
Because test-infra forces `request == limit`, the CPU request is a hard ceiling, so the
work-conserving target-avg recommendation is only safe when a build's mean is close to its
peak. This plot classifies that. For each build, the **crest factor** = `p95 / p50` of its
CPU samples (spike height vs the typical level; `p95` avoids the `rate()` max artifact).
Histogram those across builds on a **log axis** (crest is a ratio that can span 1 → thousands
when a mostly-idle build has a near-zero `p50`).

- crest **≈ 1** ⇒ flat, steady usage — **stable**; sizing to a target-average is safe.
- crest **≥ 2** (median) ⇒ tall spikes over a low baseline — **burst** (the
  idle→peak→idle→peak shape); a target-average request would clip the peak, so size such a
  job to its busy-phase demand instead.

The median across builds decides the label (`stable` / `burst`), written to
`recommendation.json` under `burstiness` alongside the p10–p90 spread (how much the builds
agree) and two companion scores: **idle fraction** (share of samples below 30% of the
build's `p95` — time spent in the valleys) and **CV** (`std/mean` — overall swinginess).

### `dist_throttle.png` — is the job CPU-starved?
Uses `container_pressure_cpu_waiting_seconds_total`. With 30s samples, take the delta from
the previous sample to get the seconds threads waited for CPU in that 30s window, then the
**% wait = waited_seconds / 30s**. For one build, take the list of % wait over all its 30s
windows and compute the fraction of windows with **% wait < 5%** — that is the **percentage
of build time running without CPU pressure**. Compute that percentage per build and
histogram it across builds.

- distribution piled near **0%** ⇒ the job is starved for CPU most of the time — **do not
  cut** its cores.
- distribution piled near **100%** ⇒ the job rarely feels CPU pressure — a candidate to
  **lower the CPU request (or limit)**.

### `timeline_throttle.png` / `timeline_stall.png` — when does demand happen?
Aligns every build by **minutes-into-build** and shows CPU cores and CPU-pressure % over
time. `timeline_throttle` uses waiting seconds (PSI "some"); `timeline_stall` uses stalled
seconds (PSI "full"). Four panels:

1. CPU cores — faint per-build cloud + median/p90/max **envelopes across builds**;
2. CPU pressure % — same population view;
3. CPU cores — a few **concrete sample builds** (real per-build shape);
4. CPU pressure % — the same sample builds.

e2e jobs typically show two demand peaks with an idle valley between (cluster bring-up,
then test execution), which is why their average is low even when peaks are high.

### `timeline_network.png` — when does network I/O happen?
The same 4-panel, minutes-into-build layout as `timeline_throttle.png`, but for network
throughput instead of CPU. Network **in** (receive) takes the role of CPU cores and network
**out** (transmit) the role of CPU pressure. Throughput per 30s interval is
Δbytes / Δt in **MiB/s** (counter resets from a restarted pod are dropped).

1. network in  — faint per-build cloud + median/p90/max **envelopes across builds**;
2. network out — same population view;
3. network in  — a few **concrete sample builds** (red/green/blue, the throttle palette);
4. network out — the same sample builds.

Image-build and e2e jobs show a receive spike early (pulling base images, Go modules,
kind node images) that maps to the compile/setup phase.
