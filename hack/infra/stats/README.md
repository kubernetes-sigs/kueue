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
    ├── recommendation.json         # structured current-vs-recommended sizing (plot.py)
    ├── dist_mean.png
    ├── dist_peak.png
    ├── dist_throttle.png
    ├── timeline_throttle.png
    └── timeline_stall.png
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

CPU "cores" = CPU-seconds of work per wall-second (a rate). Build nodes have ~7 usable
cores, so a 7-core request packs one build per node; cutting it toward 3–4 lets two share
a node.

---

## The diagrams (and how each is drawn)

`plot.py` writes five PNGs per job.

### `dist_mean.png` — sizes the CPU request
For each build, compute the **average** CPU (and memory) usage across its samples; that
gives one number per build. Histogram those numbers across all builds. Overlays: the
p50/p95/p99 across builds, the current k8s request/limit (purple), and the recommended
request/limit (gold). The bulk of the mean distribution is the sustained demand the
request should cover.

### `dist_peak.png` — sizes memory
Same as above but reduces each build to its **peak** usage. Memory is sized off these
peaks — recommended memory = **largest per-build peak × 1.15**, taken over healthy builds
only (builds that OOM-killed are excluded first, since their peak sits pinned at the
ceiling and would otherwise dictate the value). CPU peak reads inflated (a Prometheus
`rate()` extrapolation artifact over sparse samples), so it is *not* used to size CPU.

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
