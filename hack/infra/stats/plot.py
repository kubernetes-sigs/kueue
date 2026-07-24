#!/usr/bin/env python3

# Copyright The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""
plot.py — render the full image set and the sizing recommendation for ONE prow job
fetched by fetch_prow_metrics.py.

It reads the job folder's raw_series.json and writes, into that same folder:

  dist_mean.png        per-build MEAN CPU & memory histograms   -> sizes the request
  dist_peak.png        per-build PEAK CPU & memory histograms    -> sizes memory (worst case)
  dist_mean_new_cpu.png  current vs target-duration per-build mean CPU -> work-conserving reco
  dist_duration.png    per-build duration histogram (mean, percentiles, target marker)
  dist_peak_duration.png per-build peak above the cutoff: % of duration, minutes, avg CPU
  dist_work.png        per-build CPU work histogram (avg CPU × duration, core-minutes)
  dist_crest.png       per-build crest factor (p95/p50) histogram -> stable vs burst job
  dist_throttle.png    across-build histogram of "% of build time without CPU pressure"
  timeline_throttle.png  CPU cores + CPU-waiting % vs time-into-build (PSI "some")
  (timeline_stall.png — PSI "full" — is disabled; see plot_throttle_timeline)
  timeline_network.png   network in/out throughput vs time-into-build (same 4-panel layout)
  recommendation.json  structured current-vs-recommended request/limit for this job
                       (aggregate_reco.py later folds every job's JSON into one table)

Architecture
------------
Three independent renderers plus the recommendation, all built on a handful of shared
helpers. Each renderer is self-contained and degrades gracefully — the PSI-based plots
need cpu_waiting_seconds / cpu_stalled_seconds and simply skip (print + return, never
raise) when a job has too little data, so a job still gets whatever images it can.

  generate_recommendation()     -> recommendation.json         (+ returns the reco dict)
  plot_distribution()           -> dist_<stat>.png for each stat (uses the reco for lines)
  plot_new_cpu_distribution()   -> dist_mean_new_cpu.png (current vs target-duration mean CPU)
  plot_duration_distribution()  -> dist_duration.png (per-build duration histogram)
  plot_work_distribution()      -> dist_work.png (per-build CPU work histogram)
  plot_crest_distribution()     -> dist_crest.png (per-build crest factor; stable vs burst)
  plot_throttle_distribution()  -> dist_throttle.png
  plot_throttle_timeline()      -> timeline_throttle.png (timeline_stall.png disabled)
  plot_network_timeline()       -> timeline_network.png (network in/out over time-into-build)

Shared helpers:
  load()        read raw_series.json from a folder or file path
  const()       the configured (constant) request/limit for a metric
  per_build()   collapse each build's samples to one number (mean / peak / pNN)
  wait_pct()    cumulative PSI counter -> per-interval percentage (with minutes-into-build)
  cores()       cumulative cores series -> (minutes-into-build, cores)
  byte_rate()   cumulative byte counter -> (minutes-into-build, MiB/s) per interval

Examples:
  ./plot.py ./pull-kueue-test-e2e-baseline-main-1-34_30d_30s
  ./plot.py ./<dir> --only distribution           # just dist_* + recommendation.json
  ./plot.py ./<dir> --only throttle,timeline --samples 3 --seed 7
"""
import argparse, json, math, os, random
from dataclasses import dataclass
import numpy as np
import matplotlib
matplotlib.use("Agg")  # headless
import matplotlib.pyplot as plt
from matplotlib.ticker import MultipleLocator
from scipy import stats as scipy_stats

GIB = 1024 ** 3

# Bold sample-build colours for the timelines. The samples live on their own panels, so
# these do not clash with the envelope palette on the population panels.
SAMPLE_COLORS = ["#D62728", "#2CA02C", "#1F77B4", "#FF7F0E"]

# Rarely-tuned knobs, kept out of the CLI to keep the interface small.
MIN_INTERVALS = 4           # dist_throttle: skip builds with fewer PSI intervals than this
MIN_BUILDS_PER_OFFSET = 10  # timelines: draw the envelope only where >= this many builds cover an offset
MIN_SAMPLE_POINTS = 8       # timelines: only highlight sample builds with >= this many points

# CPU right-sizing (issue #12750). The sizing policy is pluggable: the CPU request is
# produced by one of the named recommenders in CPU_RECOMMENDERS, selected via
# --cpu-algorithm. The recommenders are work-conserving: they assume each build's CPU work
# (avg cores x duration) is roughly invariant to the request (CPU is compressible, so fewer
# cores just stretch the build), so a build given `work / target` cores would finish in
# about `target` minutes. The default, "target-duration-improved", refines this by fitting
# only the compressible peak work into the target budget (see the recommender's docstring),
# which keeps burst jobs from being under-provisioned. The knobs below are the defaults,
# each overridable via CLI.
CPU_TARGET_MIN = 10.0  # target build duration in minutes to size the request against
CPU_LEGROOM_FRAC = 0.0  # fractional headroom on top of the p95 target (0 = none; 0.15 = +15%)
CPU_RESOLUTION = 0.1   # round the recommendation up to this core granularity (100m)
CPU_RECURSIVE_PASSES = 5  # target-duration-improved-recursive: per-build refinement passes
CPU_MAX_CORES = 7.0    # hard ceiling for the recursive reco: a build node has ~7 usable cores
CPU_MIN_TIME_REMAIN_MIN = 1.0  # recursive: stop refining a build once its leftover target
                               # budget drops below this (minutes), to guard the peak blow-up
CPU_TAIL_OVERHEAD_MIN = 0.5    # flat estimate of the wall-clock TAIL the test-scoped metrics
                               # cannot see: artifact upload + pod teardown, which run in the
                               # `sidecar` container after the `test` container (and its CPU
                               # series) exits. The per-build overhead already captures the
                               # front (scheduling/pull) via the cpu_request_cores start; this
                               # adds the tail. ~0.5 is the median of observed (prow_wall -
                               # request_span) across jobs (range ~0.05-1.0). STOPGAP: replace
                               # with a per-job value once wall-end comes from Prow Duration or
                               # a pod-scoped metric.

# Burstiness classification. Because request == limit is a hard CPU ceiling, the
# work-conserving target-avg recommendation is only safe when a build's mean is close to its
# peak (a "stable" job). An idle->peak->idle->peak ("burst") job sized to its target-avg
# would clip the peak. We score each build's CPU sample vector three ways and classify the
# job off the median crest across its builds.
IDLE_FRAC_OF_BUSY = 0.30  # a sample below this fraction of the build's p95 counts as "idle"
CREST_STABLE_MAX = 2.0    # median crest at/below this => "stable", above => "burst"

RENDERERS = ("distribution", "throttle", "timeline")


# --------------------------------------------------------------------------- #
# Shared helpers
# --------------------------------------------------------------------------- #
def load(path):
    """Load a job's raw_series.json. `path` may be the fetch output folder or the JSON
    file itself. Returns (data, base_dir) where base_dir is where images are written."""
    json_path = os.path.join(path, "raw_series.json") if os.path.isdir(path) else path
    base_dir = path if os.path.isdir(path) else (os.path.dirname(path) or ".")
    with open(json_path) as f:
        return json.load(f), base_dir


def const(data, key, gib=False):
    """The configured request/limit for a metric (constant per build): the max value seen
    across builds, in GiB when `gib` else raw cores/bytes. None if the metric is absent."""
    for pts in data["series"].get(key, {}).values():
        if pts:
            v = max(x for _, x in pts)
            return v / GIB if gib else v
    return None


def reduce_build(vals, stat):
    """Collapse one build's samples to a single number: mean | median | peak | pNN."""
    if stat == "mean":
        return float(np.mean(vals))
    if stat == "median":
        return float(np.median(vals))
    if stat in ("peak", "max"):
        return float(np.max(vals))
    if stat.startswith("p"):
        return float(np.percentile(vals, float(stat[1:])))
    raise ValueError(f"bad stat {stat!r}; use mean, median, peak, or pNN")


def per_build(series, stat, min_dur_min=0.0, exclude=None):
    """One number per build (reduced by `stat`), dropping builds with <2 samples, shorter
    than min_dur_min minutes, or whose id is in `exclude` (e.g. OOM-killed builds).
    Returns a numpy array."""
    exclude = exclude or set()
    out = []
    for bid, pts in series.items():
        if bid in exclude or len(pts) < 2:
            continue
        ts = [t for t, _ in pts]
        if (max(ts) - min(ts)) / 60 < min_dur_min:
            continue
        out.append(reduce_build([v for _, v in pts], stat))
    return np.array(out)


def per_build_cpu_work(series, min_dur_min=0.0, exclude=None):
    """One (avg_cpu_cores, duration_min) pair per build, using the same exclusions as
    per_build (drop <2 samples, shorter than min_dur_min, or ids in `exclude`). Returning
    the pair keeps each build's usage<->duration correlation, which a marginal percentile
    over means alone would lose. Duration is the metrics-observed span of the `test`
    container, i.e. the compute phase, not prow's full wall-clock."""
    exclude = exclude or set()
    out = []
    for bid, pts in series.items():
        if bid in exclude or len(pts) < 2:
            continue
        ts = [t for t, _ in pts]
        dur = (max(ts) - min(ts)) / 60.0
        if dur < min_dur_min:
            continue
        out.append((float(np.mean([v for _, v in pts])), dur))
    return out


def target_new_avg_cpus(pairs, target_min):
    """Given per-build (avg_cpu, duration_min) pairs, the average CPU that would stretch
    each build to about target_min minutes at constant work: avg x duration / max(duration,
    target_min). The max() means a build already at/above the target keeps its real avg, so
    the transform only ever LOWERS CPU (for fast builds) and never raises it."""
    return np.array([avg * dur / max(dur, target_min) for avg, dur in pairs])


def per_build_cpu_samples(series, min_dur_min=0.0, exclude=None):
    """One numpy array of raw CPU samples per build, using the same exclusions as
    per_build_cpu_work (drop <2 samples, shorter than min_dur_min, or ids in `exclude`).
    Keeping the full sample vector lets the burstiness scores (crest / idle / CV) see the
    within-build shape that a single reduction (mean, peak) would hide."""
    exclude = exclude or set()
    out = []
    for bid, pts in series.items():
        if bid in exclude or len(pts) < 2:
            continue
        ts = [t for t, _ in pts]
        if (max(ts) - min(ts)) / 60.0 < min_dur_min:
            continue
        out.append(np.array([v for _, v in pts]))
    return out


def per_build_cpu_samples_dur(series, min_dur_min=0.0, exclude=None):
    """Like per_build_cpu_samples, but pairs each build's CPU sample vector with its
    duration in minutes. The peak-slice recommender (cpu_reco_target_duration_improved)
    needs both: the samples to split at the cutoff, and the duration to turn a below-cutoff
    sample fraction into wall-clock minutes (fixed-step sampling, so fraction x duration is
    the time the build spent below the cutoff)."""
    exclude = exclude or set()
    out = []
    for bid, pts in series.items():
        if bid in exclude or len(pts) < 2:
            continue
        ts = [t for t, _ in pts]
        dur = (max(ts) - min(ts)) / 60.0
        if dur < min_dur_min:
            continue
        out.append((np.array([v for _, v in pts]), dur))
    return out


def per_build_cpu_samples_dur_overhead(data, min_dur_min=0.0, exclude=None):
    """Like per_build_cpu_samples_dur, but also returns each build's init overhead in minutes.
    A build's cpu_request_cores series comes from the pod spec, so it appears when the pod object
    is created; cpu_used_cores comes from cAdvisor, so it appears only once the test container is
    actually burning CPU. The gap between them is wall-clock the build spent scheduling, pulling
    images, and cloning before any compute — time no amount of CPU can shorten. Overhead is that
    front gap (used_start - request_start) plus any tail gap (request_end - used_end), clamped at
    >= 0. It undercounts the full wall overhead: pre-pod queue time and post-run artifact upload
    are invisible once the pod's metrics stop (top them up via cfg.fixed_overhead_min). Builds
    with no request series contribute 0 overhead. Returns (samples, dur, overhead_min) triples."""
    exclude = exclude or set()
    used = data["series"].get("cpu_used_cores", {})
    req = data["series"].get("cpu_request_cores", {})
    out = []
    for bid, pts in used.items():
        if bid in exclude or len(pts) < 2:
            continue
        ts = [t for t, _ in pts]
        dur = (max(ts) - min(ts)) / 60.0
        if dur < min_dur_min:
            continue
        overhead = 0.0
        rpts = req.get(bid)
        if rpts:
            rts = [t for t, _ in rpts]
            overhead = max(0.0, (min(ts) - min(rts)) + (max(rts) - max(ts))) / 60.0
        out.append((np.array([v for _, v in pts]), dur, overhead))
    return out


def build_burstiness(samples):
    """Per-build (crest, idle_fraction, cv) from one build's CPU sample vector:
      crest = p95/p50   — spike height vs the typical level (avoids the rate() max artifact);
      idle  = fraction of samples below IDLE_FRAC_OF_BUSY x p95 — time spent in the valleys;
      cv    = std/mean  — overall swinginess in one number.
    A metric whose denominator is ~0 (an all-idle build) is returned as None."""
    p50 = float(np.percentile(samples, 50))
    p95 = float(np.percentile(samples, 95))
    mean = float(np.mean(samples))
    crest = p95 / p50 if p50 > 0 else None
    idle = float(np.mean(samples < IDLE_FRAC_OF_BUSY * p95)) if p95 > 0 else None
    cv = float(np.std(samples) / mean) if mean > 0 else None
    return crest, idle, cv


def per_build_burstiness(data, min_dur):
    """Per-build burstiness scores over the recommendation's build population (OOM-killed
    builds excluded, matching the sizing). Returns (crest, idle, cv) as three lists, one
    entry per scorable build. Shared by the recommendation summary and dist_crest.png so
    both see the identical population."""
    oom = oom_build_ids(data)
    builds = per_build_cpu_samples(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    crest, idle, cv = [], [], []
    for s in builds:
        c, i, v = build_burstiness(s)
        if c is not None:
            crest.append(c)
        if i is not None:
            idle.append(i)
        if v is not None:
            cv.append(v)
    return crest, idle, cv


def compute_burstiness(data, min_dur):
    """Job-level burstiness from the per-build CPU sample vectors: for each of crest, idle
    fraction and CV, the median across builds (the representative value) plus the p10-p90
    spread (how much the builds agree). The job is classified "burst" or "stable" off the
    median crest (CREST_STABLE_MAX). Returns None when there are too few builds to score."""
    crest, idle, cv = per_build_burstiness(data, min_dur)
    if len(crest) < 2:
        return None

    def agg(vals):
        if not vals:
            return None
        a = np.array(vals)
        return {"median": round(float(np.percentile(a, 50)), 3),
                "p10": round(float(np.percentile(a, 10)), 3),
                "p90": round(float(np.percentile(a, 90)), 3)}

    crest_agg = agg(crest)
    return {
        "type": "burst" if crest_agg["median"] > CREST_STABLE_MAX else "stable",
        "builds_scored": len(crest),
        "crest": crest_agg,
        "idle_fraction": agg(idle),
        "cv": agg(cv),
    }


def round_up_to(x, res):
    """Round x UP to the nearest multiple of res, so any added legroom is never rounded
    away. res=0.1 gives 100m CPU granularity."""
    return round(math.ceil(x / res) * res, 3)


def oom_build_ids(data):
    """Build ids that recorded an OOM-kill (container_oom_events_total increased). Such a
    build hit its memory limit, so its working-set peak sits at the ceiling and must not
    drive the memory recommendation. Empty when the metric was not fetched (older data)."""
    return {bid for bid, pts in data["series"].get("oom_events", {}).items()
            if pts and max(v for _, v in pts) > 0}


def wait_pct(pts):
    """Cumulative PSI counter (cpu_waiting/stalled seconds) -> (minutes_into_build, pct)
    per interval, via Δcounter/Δt clamped to [0, 100]."""
    pts = sorted(pts)
    xs, ys = [], []
    for i in range(1, len(pts)):
        dt = pts[i][0] - pts[i - 1][0]
        dv = pts[i][1] - pts[i - 1][1]
        if dt > 0 and dv >= 0:
            xs.append((pts[i][0] - pts[0][0]) / 60.0)
            ys.append(min(100.0, 100.0 * dv / dt))
    return xs, ys


def cores(pts):
    """Cumulative cores series -> (minutes_into_build, cores)."""
    pts = sorted(pts)
    if not pts:
        return [], []
    t0 = pts[0][0]
    return [(t - t0) / 60.0 for t, _ in pts], [v for _, v in pts]


def byte_rate(pts):
    """Cumulative byte counter (network rx/tx) -> (minutes_into_build, MiB/s) per interval
    via Δbytes/Δt. Counter resets (Δ<0, e.g. a restarted pod) are dropped, mirroring the
    Δ≥0 guard in wait_pct."""
    pts = sorted(pts)
    xs, ys = [], []
    MIB = 1024 ** 2
    for i in range(1, len(pts)):
        dt = pts[i][0] - pts[i - 1][0]
        dv = pts[i][1] - pts[i - 1][1]
        if dt > 0 and dv >= 0:
            xs.append((pts[i][0] - pts[0][0]) / 60.0)
            ys.append(dv / dt / MIB)
    return xs, ys


def stat_word(stat):
    return {"mean": "average", "median": "median", "peak": "peak", "max": "peak"}.get(
        stat, f"per-build {stat}")


def days_of(data):
    return round((data["end"] - data["start"]) / 86400)


# --------------------------------------------------------------------------- #
# Recommendation (derived from the usage distribution)
# --------------------------------------------------------------------------- #
@dataclass(frozen=True)
class CPURecoConfig:
    """Tunables shared by the CPU recommenders. Not every algorithm reads every field —
    e.g. "p95-mean" ignores target_min. Defaults come from the module-level constants."""
    target_min: float = CPU_TARGET_MIN
    legroom_frac: float = CPU_LEGROOM_FRAC
    resolution: float = CPU_RESOLUTION
    recursive_passes: int = CPU_RECURSIVE_PASSES  # only "target-duration-improved-recursive"
    max_cores: float = CPU_MAX_CORES              # only "target-duration-improved-recursive"
    min_time_remain_min: float = CPU_MIN_TIME_REMAIN_MIN  # only the recursive recommenders
    fixed_overhead_min: float = CPU_TAIL_OVERHEAD_MIN  # flat wall-clock TAIL the test-scoped
                                     # metrics cannot see (artifact upload + pod teardown in the
                                     # sidecar container, after the test CPU series ends). Added
                                     # on top of the per-build front overhead (cpu_request_cores
                                     # start) and subtracted from target_min. See
                                     # CPU_TAIL_OVERHEAD_MIN; 0 disables.


def cap_at_limit(data, val):
    """Never recommend more CPU than the job is already allowed: test-infra forces
    request == limit, so the current limit is a hard ceiling. Returns (capped, saturated),
    where `saturated` means the uncapped value already reached that ceiling (no room to save)."""
    cpu_lim_cur = const(data, "cpu_limit_cores")
    if cpu_lim_cur is None:
        return val, False
    return min(val, cpu_lim_cur), val >= cpu_lim_cur


def target_duration_p95(data, min_dur, cfg):
    """Shared core of the target-duration recommenders: p95 across builds of the per-build
    target mean CPU (avg x dur / max(dur, target_min); see target_new_avg_cpus), plus the
    supporting `new` and `durs` arrays. This p95 is the uncapped, un-rounded work-conserving
    CPU value — cpu_reco_target_duration rounds and caps it, while the improved recommender
    reuses it as a cutoff. Returns (p95, new, durs) or (None, None, None) when there are
    fewer than 2 usable builds."""
    oom = oom_build_ids(data)
    pairs = per_build_cpu_work(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    if len(pairs) < 2:
        return None, None, None
    new = target_new_avg_cpus(pairs, cfg.target_min)
    durs = np.array([d for _, d in pairs])
    return float(np.percentile(new, 95)), new, durs


def cpu_reco_target_duration(data, min_dur, cfg):
    """Work-conserving recommender: the single Guaranteed-QoS CPU value that would let
    builds finish in about cfg.target_min minutes, assuming CPU work (avg x duration) is
    invariant to the request. Sizes off p95 of the per-build target mean CPU (see
    target_new_avg_cpus), optionally adds fractional legroom, rounds up to cfg.resolution,
    and caps at the current limit. Returns (value, stats) or (None, None) when there is too
    little CPU data; `stats` carries the percentiles that justify it."""
    p95, new, durs = target_duration_p95(data, min_dur, cfg)
    if p95 is None:
        return None, None
    val, saturated = cap_at_limit(data, round_up_to(p95 * (1 + cfg.legroom_frac), cfg.resolution))
    stats = {
        "algorithm": "target-duration",
        "target_min": cfg.target_min,
        "legroom_frac": cfg.legroom_frac,
        "resolution": cfg.resolution,
        "saturated": saturated,
        "new_mean_p50": round(float(np.percentile(new, 50)), 3),
        "new_mean_p95": round(p95, 3),
        "new_mean_p99": round(float(np.percentile(new, 99)), 3),
        "duration_p50_min": round(float(np.percentile(durs, 50)), 2),
        "duration_p95_min": round(float(np.percentile(durs, 95)), 2),
    }
    return val, stats


def cpu_reco_p95_mean(data, min_dur, cfg):
    """Conservative recommender (the original approach): p95 of the per-build mean CPU with
    15% headroom, rounded up to whole cores, capped at the current limit. Ignores build
    duration, so it never trades runtime for cores. Returns (value, stats) or (None, None)."""
    oom = oom_build_ids(data)
    vals = per_build(data["series"].get("cpu_used_cores", {}), "mean", min_dur, exclude=oom)
    if len(vals) == 0:
        return None, None
    p95 = float(np.percentile(vals, 95))
    val, saturated = cap_at_limit(data, float(math.ceil(p95 * 1.15)))
    stats = {
        "algorithm": "p95-mean",
        "headroom_frac": 0.15,
        "saturated": saturated,
        "mean_p95": round(p95, 3),
    }
    return val, stats


def per_build_target_peak_cpu(data, min_dur, cfg):
    """Per-build peak-adjusted target CPU — the distribution the improved recommender
    (cpu_reco_target_duration_improved) takes the p95 of. For each build, the compressible
    above-cutoff (peak) work fitted into the leftover target budget:
    avg_peak x peak_dur / max(time_remain, peak_dur); a build that never crosses the cutoff
    contributes its plain per-build target mean (avg x dur / max(dur, target_min)). Also
    returns the per-build valley/peak/budget splits (only for builds that cross the cutoff,
    used for diagnostics) and the cutoff itself. Returns
    (per_reco, below_mins, peak_durs, time_remains, cutoff) or five Nones when the cutoff
    cannot be computed or fewer than two builds qualify."""
    cutoff, _, _ = target_duration_p95(data, min_dur, cfg)
    if cutoff is None:
        return None, None, None, None, None
    oom = oom_build_ids(data)
    builds = per_build_cpu_samples_dur(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)

    per_reco = []
    below_mins, peak_durs, time_remains = [], [], []
    for samples, dur in builds:
        peak = samples[samples > cutoff]
        if len(peak) == 0:
            # Never exceeds the cutoff: the whole run is "valley", so its own target mean
            # already fits — contribute the plain per-build value.
            per_reco.append(float(np.mean(samples)) * dur / max(dur, cfg.target_min))
            continue
        below_min = float(np.mean(samples <= cutoff)) * dur
        peak_dur = dur - below_min
        time_remain = cfg.target_min - below_min
        per_reco.append(float(np.mean(peak)) * peak_dur / max(time_remain, peak_dur))
        below_mins.append(below_min)
        peak_durs.append(peak_dur)
        time_remains.append(time_remain)

    if len(per_reco) < 2:
        return None, None, None, None, None
    return np.array(per_reco), below_mins, peak_durs, time_remains, cutoff


def cpu_reco_target_duration_improved(data, min_dur, cfg):
    """Peak-aware work-conserving recommender (the default). Plain target-duration sizes off
    each build's *mean*, which under-provisions burst jobs: their idle valleys (cluster
    bring-up, network/I-O waits) are incompressible wall-clock that more cores cannot
    shorten, so folding them into the average hides the true peak demand — and since
    request == limit is a hard ceiling, the request then clips the peak.

    This variant separates the two. It takes the plain target-duration value as a CPU
    cutoff, treats each build's below-cutoff time as a fixed cost, and fits only the
    compressible above-cutoff (peak) work into whatever of the cfg.target_min budget the
    valleys leave:

        below_min   = (fraction of samples <= cutoff) x duration   # fixed valley time
        peak_dur    = duration - below_min                         # time spent in peaks
        time_remain = cfg.target_min - below_min                   # budget left for peaks
        peak_reco   = avg_peak x peak_dur / max(time_remain, peak_dur)

    max(time_remain, peak_dur) mirrors plain target-duration's max(dur, target_min): never
    compress the peak below its real duration (and never divide by <= 0 when the valleys
    alone exceed the target). A build that never crosses the cutoff has no peak slice and
    contributes its plain per-build value, so on a stable job this collapses back to
    cpu_reco_target_duration. Aggregates p95 across builds, adds legroom, rounds up, caps at
    the current limit. Returns (value, stats) or (None, None)."""
    reco, below_mins, peak_durs, time_remains, cutoff = per_build_target_peak_cpu(data, min_dur, cfg)
    if reco is None:
        return None, None
    p95 = float(np.percentile(reco, 95))
    val, saturated = cap_at_limit(data, round_up_to(p95 * (1 + cfg.legroom_frac), cfg.resolution))

    def p50(vals):
        return round(float(np.percentile(vals, 50)), 2) if vals else None

    stats = {
        "algorithm": "target-duration-improved",
        "target_min": cfg.target_min,
        "legroom_frac": cfg.legroom_frac,
        "resolution": cfg.resolution,
        "saturated": saturated,
        "cutoff": round(cutoff, 3),
        "builds_with_peak": len(below_mins),
        "peak_reco_p50": round(float(np.percentile(reco, 50)), 3),
        "peak_reco_p95": round(p95, 3),
        "peak_reco_p99": round(float(np.percentile(reco, 99)), 3),
        "below_cutoff_min_p50": p50(below_mins),
        "peak_duration_min_p50": p50(peak_durs),
        "time_remain_min_p50": p50(time_remains),
    }
    return val, stats


def per_build_peak_duration(data, min_dur, cfg):
    """Per-build peak split at the improved recommender's CPU cutoff (see
    cpu_reco_target_duration_improved): the cutoff is the plain target-duration value, and a
    build's "peak" is the samples above it. Returns (peak_pct, peak_min, avg_peak, cutoff):
      peak_pct  — peak time as a percentage of the build's duration (one entry per build);
      peak_min  — peak time in minutes, fraction-of-samples-above x duration (per build);
      avg_peak  — mean of the above-cutoff samples, i.e. the busy-phase average CPU that the
                  recommender fits into the budget (only builds that cross the cutoff, so
                  this array is shorter than the other two).
    Returns (None, None, None, None) when the cutoff cannot be computed. Builds that never
    cross the cutoff contribute 0 to peak_pct/peak_min (matching the recommender's "whole run
    is valley" case) and nothing to avg_peak (no peak to average)."""
    cutoff, _, _ = target_duration_p95(data, min_dur, cfg)
    if cutoff is None:
        return None, None, None, None
    oom = oom_build_ids(data)
    builds = per_build_cpu_samples_dur(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    pct, mins, avg_peak = [], [], []
    for samples, dur in builds:
        above = samples > cutoff
        frac_above = float(np.mean(above))
        pct.append(100.0 * frac_above)
        mins.append(frac_above * dur)
        if frac_above > 0:
            avg_peak.append(float(np.mean(samples[above])))
    return np.array(pct), np.array(mins), np.array(avg_peak), cutoff


def recursive_target_cpu(samples, dur, cfg, overhead_min=0.0):
    """One build's self-consistent target CPU. cpu_reco_target_duration_improved splits a build
    into valley (<= cutoff) and peak (> cutoff) once, at the plain target-duration value, and
    fits the peak work into the leftover target budget; but that cutoff is the CPU it is trying
    to compute, so the split is only as good as the seed. This iterates it: the recommended CPU
    becomes the next pass's cutoff, converging the split on the value actually recommended.

    Each pass, at the current cutoff `cpu`:
        below_min   = (fraction of samples <= cpu) x dur    # incompressible valley time
        time_remain = budget - below_min                    # budget left for the peak
        cpu         = avg(samples > cpu) x (dur - below_min) / time_remain

    Seeded from below_min=0 (cpu = avg x dur / budget, with no max(dur, budget) floor,
    so a build slower than the budget is raised rather than left alone). The value climbs
    monotonically toward the fixed point and the loop stops as soon as a pass no longer raises
    it. Two guards keep it from running away once the cutoff has eaten most of the build: if
    nothing is left above the cutoff, or the leftover budget drops below cfg.min_time_remain_min
    minutes (only an instantaneous spike remains), the climb halts. cfg.max_cores bounds the
    final value.

    `budget` is cfg.target_min minus this build's own init overhead: work only occupies the
    compute-span, but the target is wall-clock, so the fixed cost spent outside the span is first
    carved off the target. `overhead_min` is the build's measured init overhead (the gap between
    the pod object appearing and the test container burning CPU; see
    per_build_cpu_samples_dur_overhead); cfg.fixed_overhead_min is an additional flat top-up for
    the parts the metrics cannot see (pre-pod queue time, post-run artifact upload). If the
    overhead alone blows the target (budget below min_time_remain_min), the build cannot hit the
    target at any CPU, so we return the ceiling to signal that."""
    budget = cfg.target_min - overhead_min - cfg.fixed_overhead_min
    if budget < cfg.min_time_remain_min:
        return cfg.max_cores  # fixed overhead alone exceeds the target; no CPU can hit it
    cpu = float(np.mean(samples)) * dur / budget  # pass 1: below_min = 0
    for _ in range(max(0, cfg.recursive_passes - 1)):
        peak = samples[samples > cpu]
        below_min = float(np.mean(samples <= cpu)) * dur
        time_remain = budget - below_min
        if len(peak) == 0 or time_remain < cfg.min_time_remain_min:
            break  # only a spike (or nothing) left above the cutoff; stop before it runs away
        nxt = float(np.mean(peak)) * (dur - below_min) / time_remain
        if nxt <= cpu:
            break  # converged
        cpu = nxt
    return min(cpu, cfg.max_cores)


def cpu_reco_target_duration_improved_recursive(data, min_dur, cfg):
    """Recursive peak-aware recommender: like cpu_reco_target_duration_improved, but iterates
    each build's valley/peak split to a fixed point (see recursive_target_cpu) rather than
    splitting once, and then aggregates p95 across builds, adds legroom, and rounds up.

    Two deliberate differences from the other recommenders: it is UNCAPPED against the
    current k8s limit (it does not call cap_at_limit), so it can recommend MORE CPU than the
    job is allowed today when builds genuinely need it to hit the target; and it drops the
    downward-only floors so it can raise as well as lower. Runaway is instead prevented per
    build by the cfg.min_time_remain_min budget guard in recursive_target_cpu; cfg.max_cores
    (the ~7-core build node) bounds the final value. Returns (value, stats) or (None, None)
    when there are fewer than two usable builds."""
    oom = oom_build_ids(data)
    builds = per_build_cpu_samples_dur_overhead(data, min_dur, exclude=oom)
    if len(builds) < 2:
        return None, None
    per = np.array([recursive_target_cpu(s, d, cfg, overhead_min=oh) for s, d, oh in builds])
    overheads = np.array([oh for _, _, oh in builds])
    p95 = float(np.percentile(per, 95))
    val = min(round_up_to(p95 * (1 + cfg.legroom_frac), cfg.resolution), cfg.max_cores)
    stats = {
        "algorithm": "target-duration-improved-recursive",
        "target_min": cfg.target_min,
        "legroom_frac": cfg.legroom_frac,
        "resolution": cfg.resolution,
        "passes": cfg.recursive_passes,
        "max_cores": cfg.max_cores,
        "fixed_overhead_min": cfg.fixed_overhead_min,
        "saturated": val >= cfg.max_cores,
        "reco_p50": round(float(np.percentile(per, 50)), 3),
        "reco_p95": round(p95, 3),
        "reco_p99": round(float(np.percentile(per, 99)), 3),
        "overhead_min_p50": round(float(np.percentile(overheads, 50)), 3),
        "overhead_min_p95": round(float(np.percentile(overheads, 95)), 3),
        "builds_at_ceiling": int(np.sum(per >= cfg.max_cores)),
    }
    return val, stats


# Pluggable CPU sizing policies. Add an algorithm here and it becomes selectable via
# --cpu-algorithm with no other wiring. Each maps (data, min_dur, cfg) -> (value, stats).
CPU_RECOMMENDERS = {
    "target-duration-improved": cpu_reco_target_duration_improved,
    "target-duration-improved-recursive": cpu_reco_target_duration_improved_recursive,
    "target-duration": cpu_reco_target_duration,
    "p95-mean": cpu_reco_p95_mean,
}
DEFAULT_CPU_ALGORITHM = "target-duration-improved-recursive"


def compute_reco(data, min_dur, cfg=None, algorithm=DEFAULT_CPU_ALGORITHM):
    """Recommended request/limit from the usage distribution:
      cpu request = the chosen recommender's value (see CPU_RECOMMENDERS / --cpu-algorithm),
        capped at the current limit; the sizing policy is plug-and-play;
      cpu limit   = equal to the cpu request (Guaranteed QoS: request == limit);
      mem request == mem limit = largest observed per-build peak x1.15. Memory is
        incompressible, so the value must hold the worst peak. Builds that OOM-killed are
        excluded first (their peak sits pinned at the ceiling), so the max is taken over
        healthy builds only and is not polluted by those artifacts.
    Returns the reco dict, or None if the job has too little CPU/memory data to size."""
    cfg = cfg or CPURecoConfig()
    oom = oom_build_ids(data)

    def xp(key, stat, p, gib):
        vals = per_build(data["series"].get(key, {}), stat, min_dur, exclude=oom)
        if gib:
            vals = vals / GIB
        return float(np.percentile(vals, p)) if len(vals) else None

    mem_peak_max = xp("mem_used_bytes", "peak", 100, True)
    cpu_val, cpu_stats = CPU_RECOMMENDERS[algorithm](data, min_dur, cfg)
    if cpu_val is None or mem_peak_max is None:
        return None

    cpu_lim = cpu_val
    mem_val = math.ceil(mem_peak_max * 1.15)
    return {
        "cpu_algorithm": algorithm,
        "cpu_req": cpu_val,
        "cpu_lim": cpu_lim,
        "cpu_stats": cpu_stats,
        "mem_req": mem_val,
        "mem_lim": mem_val,
        "saturated": cpu_stats["saturated"],
    }


def build_reco_json(data, reco, min_dur):
    """Assemble the structured per-job recommendation (current vs recommended values,
    savings, and the supporting usage percentiles). CPU in cores, memory in GiB."""
    oom = oom_build_ids(data)

    def xp(key, stat, p, gib):
        vals = per_build(data["series"].get(key, {}), stat, min_dur, exclude=oom)
        if gib:
            vals = vals / GIB
        return round(float(np.percentile(vals, p)), 3) if len(vals) else None

    def saved(cur, rec):
        return round(cur - rec, 3) if cur is not None and rec is not None else None

    n = len(per_build(data["series"].get("mem_used_bytes", {}), "mean", min_dur, exclude=oom))
    cpu_req_cur, mem_req_cur = const(data, "cpu_request_cores"), const(data, "mem_request_bytes", True)
    durs = [d for _, d in per_build_cpu_work(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)]
    return {
        "job": data.get("job"),
        "range_days": round((data["end"] - data["start"]) / 86400, 2),
        "step_s": data.get("step"),
        "builds": n,
        "builds_oom_excluded": len(oom),
        "avg_duration_min": round(float(np.mean(durs)), 2) if durs else None,
        "burstiness": compute_burstiness(data, min_dur),
        "cpu": {
            "algorithm": reco["cpu_algorithm"],
            "request_current": cpu_req_cur,
            "request_recommended": reco["cpu_req"],
            "limit_current": const(data, "cpu_limit_cores"),
            "limit_recommended": reco["cpu_lim"],
            "saved": saved(cpu_req_cur, reco["cpu_req"]),
            "mean_p50": xp("cpu_used_cores", "mean", 50, False),
            "mean_p95": xp("cpu_used_cores", "mean", 95, False),
            "mean_p99": xp("cpu_used_cores", "mean", 99, False),
            "peak_max": xp("cpu_used_cores", "peak", 100, False),
            "saturated": reco["saturated"],
            "stats": reco["cpu_stats"],
        },
        "mem": {
            "request_current": mem_req_cur,
            "request_recommended": reco["mem_req"],
            "limit_current": const(data, "mem_limit_bytes", True),
            "limit_recommended": reco["mem_lim"],
            "saved": saved(mem_req_cur, reco["mem_req"]),
            "peak_gib": xp("mem_used_bytes", "peak", 100, True),
        },
    }


def generate_recommendation(data, base_dir, min_dur, cfg=None,
                            algorithm=DEFAULT_CPU_ALGORITHM):
    """Compute the sizing recommendation and write recommendation.json. Returns the reco
    dict (or None, printing a skip note, when the job has too little data)."""
    reco = compute_reco(data, min_dur, cfg, algorithm)
    if reco is None:
        print("  recommendation: skipped (not enough CPU/memory data)")
        return None
    out = os.path.join(base_dir, "recommendation.json")
    with open(out, "w") as f:
        json.dump(build_reco_json(data, reco, min_dur), f, indent=2)
    print(f"  recommendation -> {out}")
    return reco


# --------------------------------------------------------------------------- #
# Renderer 1: usage distributions (dist_mean.png, dist_peak.png)
# --------------------------------------------------------------------------- #
def _hist_on_ax(ax, vals, xlabel, title, bins=30,
                req=None, lim=None, rec_req=None, rec_lim=None):
    """Histogram of the already-reduced per-build `vals` with a KDE bell curve, p50/p95/p99
    lines, the current k8s request/limit (purple) and, when given, the recommended ones
    (gold). Shared by every distribution plot so they stay visually identical."""
    if len(vals) < 2:
        ax.text(0.5, 0.5, "not enough data", ha="center", transform=ax.transAxes)
        return
    p50, p95, p99 = np.percentile(vals, [50, 95, 99])
    _, edges, _ = ax.hist(vals, bins=bins, color="#4C78A8", alpha=0.75,
                          edgecolor="white", label=f"builds (n={len(vals)})")
    if vals.std() > 0:  # KDE scaled back up to counts
        xs = np.linspace(vals.min(), vals.max(), 400)
        ax.plot(xs, scipy_stats.gaussian_kde(vals)(xs) * len(vals) * (edges[1] - edges[0]),
                color="#111", lw=2, label="KDE (bell curve)")
    for x, c, lbl in [(p50, "#2CA02C", f"p50={p50:.2f}"), (p95, "#FF7F0E", f"p95={p95:.2f}"),
                      (p99, "#D62728", f"p99={p99:.2f}")]:
        ax.axvline(x, color=c, ls="--", lw=1.8, label=lbl)
    if req is not None:
        ax.axvline(req, color="#8E44AD", ls="-", lw=2.5, alpha=0.85, label=f"k8s request={req:g}")
    if lim is not None:
        eq = " (=request)" if req is not None and lim == req else ""
        ax.axvline(lim, color="#8E44AD", ls=":", lw=2.5, alpha=0.85, label=f"k8s limit={lim:g}{eq}")
    if rec_req is not None:
        ax.axvline(rec_req, color="gold", ls="-", lw=2.5, label=f"optimal request={rec_req:g}")
    if rec_lim is not None:
        eq = " (=request)" if rec_req is not None and rec_lim == rec_req else ""
        ax.axvline(rec_lim, color="gold", ls=":", lw=2.5, label=f"optimal limit={rec_lim:g}{eq}")
    ax.set_xlabel(xlabel)
    ax.set_ylabel("number of builds")
    ax.set_title(title)
    ax.legend(fontsize=8, framealpha=0.9)
    ax.grid(axis="y", alpha=0.3)


def _draw_hist(ax, series, unit, stat, bins, min_dur, scale=1.0,
               req=None, lim=None, rec_req=None, rec_lim=None):
    """Reduce each build to `stat` (scaled), then histogram via _hist_on_ax."""
    vals = per_build(series, stat, min_dur) * scale
    sw = stat_word(stat)
    _hist_on_ax(ax, vals, f"per-build {sw} {unit}",
                f"distribution across {len(vals)} builds of each build's {sw} {unit}",
                bins=bins, req=req, lim=lim, rec_req=rec_req, rec_lim=rec_lim)


def plot_distribution(data, base_dir, reco, bins=30, min_dur=0.0, stats=("mean", "peak")):
    """Render dist_<stat>.png (stacked CPU + memory histograms) for each requested stat.
    `reco` (or None) supplies the recommended request/limit overlay lines."""
    S = data["series"]
    cpu_req, cpu_lim = const(data, "cpu_request_cores"), const(data, "cpu_limit_cores")
    mem_req, mem_lim = const(data, "mem_request_bytes", True), const(data, "mem_limit_bytes", True)
    rc = reco or {}
    for stat in stats:
        fig, axes = plt.subplots(2, 1, figsize=(11, 5.4 * 2))
        _draw_hist(axes[0], S.get("cpu_used_cores", {}), "CPU cores", stat, bins, min_dur, 1.0,
                   cpu_req, cpu_lim, rc.get("cpu_req"), rc.get("cpu_lim"))
        _draw_hist(axes[1], S.get("mem_used_bytes", {}), "memory GiB", stat, bins, min_dur, 1 / GIB,
                   mem_req, mem_lim, rc.get("mem_req"), rc.get("mem_lim"))
        fig.suptitle(f"{data.get('job')}  —  {stat_word(stat)}  —  {data.get('step')}s step, "
                     f"{days_of(data)}d", fontsize=13, y=0.995)
        fig.tight_layout(rect=[0, 0, 1, 0.99])
        out = os.path.join(base_dir, f"dist_{stat}.png")
        fig.savefig(out, dpi=130)
        plt.close(fig)
        print(f"  dist_{stat} -> {out}")


def _draw_duration_hist(ax, durs, target_min, bins=30):
    """Per-build duration histogram (minutes) with p50/p95/p99, the mean, and the gold
    target marker. Shared by dist_duration.png and the bottom panel of dist_mean_new_cpu.png
    so the two stay identical."""
    _hist_on_ax(ax, durs, "per-build duration (minutes)",
                f"build durations across {len(durs)} builds "
                f"(builds ≥ {target_min:g} min keep their CPU unchanged)", bins=bins)
    if len(durs) >= 2:
        ax.axvline(float(np.mean(durs)), color="#1F77B4", ls="-.", lw=2,
                   label=f"mean = {np.mean(durs):.2f} min")
    ax.axvline(target_min, color="gold", ls="-", lw=2.5, label=f"target = {target_min:g} min")
    ax.legend(fontsize=8, framealpha=0.9)  # redraw so the mean + target lines are included


def plot_new_cpu_distribution(data, base_dir, reco, bins=30, min_dur=0.0,
                              target_min=CPU_TARGET_MIN, cfg=None):
    """Render dist_mean_new_cpu.png: three stacked panels.
      top    — each build's CURRENT mean CPU (today's demand);
      middle — each build's TARGET mean CPU = work / max(duration, target_min), i.e. the
               request that would stretch the build to ~target_min minutes at constant work;
      bottom — the per-build DURATION distribution (minutes), with the target marked, which
               explains the shift above: builds already at/above the target are left alone.
    The top two panels share x and y so the leftward CPU shift is obvious; the duration
    panel is on its own minutes axis. The middle panel marks two target-duration sizings at
    their raw p95 basis (un-rounded, so they land on the distribution's p95): the ORIGIN
    (plain target-duration) and the peak-ADJUSTED value (the improved recommender, which
    lifts the origin to cover the compressible peak rather than the whole-build average).
    Each label shows the actual recommended request — round_up_to resolution, capped at the
    limit — in parentheses. The gap between the two is what accounting for peaks buys.
    OOM-killed builds are excluded throughout."""
    cfg = cfg or CPURecoConfig(target_min=target_min)
    oom = oom_build_ids(data)
    pairs = per_build_cpu_work(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    if len(pairs) < 2:
        print("  dist_mean_new_cpu: skipped (not enough CPU data)")
        return
    old = np.array([avg for avg, _ in pairs])
    new = target_new_avg_cpus(pairs, target_min)
    durs = np.array([d for _, d in pairs])
    cpu_req_cur, cpu_lim_cur = const(data, "cpu_request_cores"), const(data, "cpu_limit_cores")
    # The two target-duration sizings, computed independently of --cpu-algorithm so this
    # panel always contrasts them: origin (plain) vs peak-adjusted (improved). Each line is
    # drawn at its RAW p95 basis (with legroom, before round-up/cap) so it lands exactly on
    # the distribution's p95 rather than at the ceiled 0.1-core recommendation; the actual
    # recommended request (round_up_to + limit cap) is shown in parentheses.
    lg = 1 + cfg.legroom_frac
    origin_val, origin_stats = cpu_reco_target_duration(data, min_dur, cfg)
    adj_val, adj_stats = cpu_reco_target_duration_improved(data, min_dur, cfg)
    origin_raw = origin_stats["new_mean_p95"] * lg if origin_stats else None
    adj_raw = adj_stats["peak_reco_p95"] * lg if adj_stats else None
    # The per-build peak-adjusted distribution the improved recommender takes p95 of (adj_raw
    # is its p95, with legroom). Shown as its own panel so the gold line above has a visible
    # distribution behind it, not just a bare vertical mark.
    peak_reco = per_build_target_peak_cpu(data, min_dur, cfg)[0]

    # The three CPU panels share bin edges + x/y so their geometry is identical and
    # comparable; the duration panel (bottom) lives on its own minutes axis, so it is not
    # shared.
    xmax = max(old.max(), float(new.max()), cpu_req_cur or 0.0, origin_raw or 0.0,
               adj_raw or 0.0, float(peak_reco.max()) if peak_reco is not None else 0.0) * 1.05 or 1.0
    edges = np.linspace(0, xmax, bins + 1)
    fig = plt.figure(figsize=(11, 5.4 * 4))
    ax_cur = fig.add_subplot(4, 1, 1)
    ax_tgt = fig.add_subplot(4, 1, 2, sharex=ax_cur, sharey=ax_cur)
    ax_peak = fig.add_subplot(4, 1, 3, sharex=ax_cur, sharey=ax_cur)
    ax_dur = fig.add_subplot(4, 1, 4)
    _hist_on_ax(ax_cur, old, "per-build average CPU cores",
                f"current — distribution across {len(old)} builds of each build's average CPU",
                bins=edges, req=cpu_req_cur, lim=cpu_lim_cur)
    _hist_on_ax(ax_tgt, new, "per-build target-duration CPU cores",
                f"target {target_min:g} min — origin (plain) vs peak-adjusted p95",
                bins=edges, req=cpu_req_cur)
    # Origin (plain target-duration) and peak-adjusted (improved) p95 lines. Drawn here
    # rather than via _hist_on_ax's single gold rec_req so the two are visually distinct.
    if origin_raw is not None:
        ax_tgt.axvline(origin_raw, color="#17BECF", ls="-.", lw=2.4,
                       label=f"origin p95 = {origin_raw:.3g}  (rec {origin_val:g})")
    if adj_raw is not None:
        ax_tgt.axvline(adj_raw, color="gold", ls="-", lw=2.5,
                       label=f"peak-adjusted p95 = {adj_raw:.3g}  (rec {adj_val:g})")
    ax_tgt.legend(fontsize=8, framealpha=0.9)  # redraw so the two request lines are included

    # New panel: the per-build peak-adjusted distribution itself, marking its p95 (= the
    # improved recommendation basis) so the reader sees where the gold line above comes from.
    if peak_reco is not None:
        _hist_on_ax(ax_peak, peak_reco, "per-build peak-adjusted CPU cores",
                    f"target {target_min:g} min — peak-adjusted per-build CPU "
                    f"(busy-phase work fit into the leftover budget)",
                    bins=edges, req=cpu_req_cur)
        if adj_raw is not None:
            ax_peak.axvline(adj_raw, color="gold", ls="-", lw=2.5,
                            label=f"peak-adjusted p95 = {adj_raw:.3g}  (rec {adj_val:g})")
            ax_peak.legend(fontsize=8, framealpha=0.9)  # redraw so the request line is included
    else:
        ax_peak.text(0.5, 0.5, "not enough data", ha="center", transform=ax_peak.transAxes)
    ax_cur.set_xlim(0, xmax)

    _draw_duration_hist(ax_dur, durs, target_min, bins)

    fig.suptitle(f"{data.get('job')}  —  current vs {target_min:g}-min-target mean CPU  —  "
                 f"{data.get('step')}s step, {days_of(data)}d", fontsize=13, y=0.995)
    fig.tight_layout(rect=[0, 0, 1, 0.99])
    out = os.path.join(base_dir, "dist_mean_new_cpu.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  dist_mean_new_cpu -> {out}"
          + (f"  (origin p95 {origin_raw:.3g}→rec {origin_val:g}, "
             f"peak-adjusted p95 {adj_raw:.3g}→rec {adj_val:g} cores)"
             if origin_raw is not None and adj_raw is not None else ""))


def plot_duration_distribution(data, base_dir, bins=30, min_dur=0.0,
                               target_min=CPU_TARGET_MIN):
    """Render dist_duration.png: the per-build duration histogram on its own image — the
    same panel shown at the bottom of dist_mean_new_cpu.png, with mean/percentiles and the
    target marker. OOM-killed builds are excluded, matching the recommendation."""
    oom = oom_build_ids(data)
    pairs = per_build_cpu_work(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    if len(pairs) < 2:
        print("  dist_duration: skipped (not enough CPU data)")
        return
    durs = np.array([d for _, d in pairs])
    fig, ax = plt.subplots(figsize=(12, 6.5))
    _draw_duration_hist(ax, durs, target_min, bins)
    fig.suptitle(f"{data.get('job')}  —  per-build duration  —  {data.get('step')}s step, "
                 f"{days_of(data)}d", fontsize=13)
    fig.tight_layout()
    out = os.path.join(base_dir, "dist_duration.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  dist_duration -> {out}")


def plot_peak_duration_distribution(data, base_dir, bins=30, min_dur=0.0, cfg=None):
    """Render dist_peak_duration.png: three stacked histograms describing each build's peak —
    the samples above the improved recommender's CPU cutoff (see per_build_peak_duration /
    cpu_reco_target_duration_improved).
      top    — peak time as a percentage of the build's duration (how bursty the shape is);
      middle — peak time in minutes (how long the compressible work actually lasts);
      bottom — average peak CPU (mean of the above-cutoff samples): the busy-phase demand the
               recommender fits into the remaining budget. Only builds that cross the cutoff
               appear here (a build entirely in the valley has no peak to average), so its
               build count can be lower than the top two panels'.
    Together they show the split the improved algorithm relies on: a low percentage with a
    short peak means most of the build is incompressible valley. OOM-killed builds are
    excluded, matching the recommendation."""
    cfg = cfg or CPURecoConfig()
    pct, mins, avg_peak, cutoff = per_build_peak_duration(data, min_dur, cfg)
    if pct is None or len(pct) < 2:
        print("  dist_peak_duration: skipped (not enough CPU data)")
        return
    fig, (ax_pct, ax_min, ax_avg) = plt.subplots(3, 1, figsize=(11, 5.4 * 3))
    _hist_on_ax(ax_pct, pct, "per-build peak time (% of build duration)",
                f"distribution across {len(pct)} builds of time spent above the "
                f"cutoff ({cutoff:.2f} cores)", bins=bins)
    _hist_on_ax(ax_min, mins, "per-build peak time (minutes)",
                f"distribution across {len(mins)} builds of peak duration in minutes",
                bins=bins)
    _hist_on_ax(ax_avg, avg_peak, "per-build average peak CPU cores",
                f"distribution across {len(avg_peak)} builds with a peak of the "
                f"above-cutoff average CPU", bins=bins)
    ax_avg.axvline(cutoff, color="#8E44AD", ls="-", lw=2.0, alpha=0.85,
                   label=f"cutoff = {cutoff:.2f}")
    ax_avg.legend(fontsize=8, framealpha=0.9)  # redraw so the cutoff reference is included
    fig.suptitle(f"{data.get('job')}  —  peak above {cutoff:.2f}-core cutoff  —  "
                 f"{data.get('step')}s step, {days_of(data)}d", fontsize=13, y=0.995)
    fig.tight_layout(rect=[0, 0, 1, 0.99])
    out = os.path.join(base_dir, "dist_peak_duration.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  dist_peak_duration -> {out}")


def plot_work_distribution(data, base_dir, bins=30, min_dur=0.0):
    """Render dist_work.png: the per-build CPU work histogram (avg CPU x duration, in
    core-minutes). Work is the quantity the recommendation assumes is invariant to the
    request, so its spread across builds shows how stable that assumption is for the job.
    OOM-killed builds are excluded, matching the recommendation."""
    oom = oom_build_ids(data)
    pairs = per_build_cpu_work(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    if len(pairs) < 2:
        print("  dist_work: skipped (not enough CPU data)")
        return
    work = np.array([avg * dur for avg, dur in pairs])
    fig, ax = plt.subplots(figsize=(12, 6.5))
    _hist_on_ax(ax, work, "per-build CPU work (core-minutes)",
                f"CPU work across {len(work)} builds of each build's avg CPU × duration",
                bins=bins)
    ax.axvline(float(np.mean(work)), color="#1F77B4", ls="-.", lw=2,
               label=f"mean = {np.mean(work):.1f} core-min")
    ax.legend(fontsize=8, framealpha=0.9)  # redraw so the mean line is included
    fig.suptitle(f"{data.get('job')}  —  per-build CPU work  —  {data.get('step')}s step, "
                 f"{days_of(data)}d", fontsize=13)
    fig.tight_layout()
    out = os.path.join(base_dir, "dist_work.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  dist_work -> {out}")


def plot_crest_distribution(data, base_dir, bins=30, min_dur=0.0):
    """Render dist_crest.png: each build's crest factor (p95/p50 of its CPU samples)
    histogrammed across builds. Crest measures spike height vs the typical level — ~1 is
    flat/stable usage, >= CREST_STABLE_MAX is bursty (idle->peak->idle->peak). The gold line
    marks the stable/burst threshold; the median (blue) decides the job's classification,
    stated in the title. A wide p10-p90 gap means the builds disagree on burstiness."""
    crest, _, _ = per_build_burstiness(data, min_dur)
    crest = np.array(crest)
    if len(crest) < 2:
        print("  dist_crest: skipped (not enough CPU data)")
        return
    p10, p50, p90 = np.percentile(crest, [10, 50, 90])
    label = "burst" if p50 > CREST_STABLE_MAX else "stable"
    # Log x-axis: crest is a ratio, so equal multiplicative steps (2->4, 4->8) should read as
    # equal distances, and the per-job range is huge (a near-zero-p50 build can push crest into
    # the thousands) — on linear that lone outlier crushes the bulk into one bin. Keep the
    # default power-of-ten ticks (10^0, 10^1, ...): the scientific labels themselves signal to
    # the reader that the axis is logarithmic, without picking arbitrary data-dependent numbers.
    fig, ax = plt.subplots(figsize=(12, 6.5))
    lo = max(1.0, float(crest.min()))
    edges = np.logspace(np.log10(lo), np.log10(crest.max()), bins + 1)
    ax.hist(crest, bins=edges, color="#4C78A8", alpha=0.75, edgecolor="white",
            label=f"builds (n={len(crest)})")
    ax.set_xscale("log")
    ax.set_xlim(lo / 1.05, max(float(crest.max()), CREST_STABLE_MAX) * 1.05)  # keep threshold in view
    ax.axvline(p50, color="#1F77B4", ls="--", lw=2.2, label=f"median = {p50:.2f}")
    ax.axvline(p10, color="#2CA02C", ls="--", lw=1.6, label=f"p10 = {p10:.2f}")
    ax.axvline(p90, color="#D62728", ls="--", lw=1.6, label=f"p90 = {p90:.2f}")
    ax.axvline(CREST_STABLE_MAX, color="gold", ls="-", lw=2.5,
               label=f"stable/burst threshold = {CREST_STABLE_MAX:g}")
    ax.set_xlabel("per-build crest factor  (p95 / p50 of the build's CPU samples, log scale)")
    ax.set_ylabel("number of builds")
    ax.set_title(f"{data.get('job')} — CPU crest factor across builds  →  {label.upper()}\n"
                 f"({data.get('step')}s step, {days_of(data)}d, n={len(crest)})")
    ax.legend(fontsize=9)
    ax.grid(axis="y", alpha=0.3)
    fig.tight_layout()
    out = os.path.join(base_dir, "dist_crest.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  dist_crest -> {out}  ({label}, median crest {p50:.2f})")


# --------------------------------------------------------------------------- #
# Renderer 2: "without CPU pressure" distribution (dist_throttle.png)
# --------------------------------------------------------------------------- #
def plot_throttle_distribution(data, base_dir, threshold=5.0, bins=30):
    """Render dist_throttle.png: for each build, the % of its 30s intervals that ran
    without CPU pressure (per-interval CPU wait% below `threshold`), histogrammed across
    builds. Mass near 100% => rarely starved (safe to cut cores); near 0% => protect it."""
    wsrc = data["series"].get("cpu_waiting_seconds")
    if not wsrc:
        print("  dist_throttle: skipped (no cpu_waiting_seconds)")
        return
    loose_frac = []  # per build: % of intervals working without CPU pressure
    for pts in wsrc.values():
        _, ys = wait_pct(pts)
        if len(ys) < MIN_INTERVALS:
            continue
        loose_frac.append(100.0 * sum(1 for v in ys if v < threshold) / len(ys))
    loose = np.array(loose_frac)
    if len(loose) < 2:
        print("  dist_throttle: skipped (not enough builds)")
        return

    p10, p50, mean = np.percentile(loose, 10), np.percentile(loose, 50), loose.mean()
    fig, ax = plt.subplots(figsize=(12, 6.5))
    ax.hist(loose, bins=bins, range=(0, 100), color="#2CA02C", alpha=0.75,
            edgecolor="white", label=f"builds (n={len(loose)})")
    ax.axvline(mean, color="#111", ls="--", lw=2, label=f"mean = {mean:.0f}%")
    ax.axvline(p50, color="#1F77B4", ls="--", lw=1.8, label=f"median = {p50:.0f}%")
    ax.axvline(p10, color="#D62728", ls="--", lw=1.8, label=f"p10 (worst tenth) = {p10:.0f}%")
    ax.set_xlabel(f"% of build time working without CPU pressure  (CPU wait % < {threshold:g}%)")
    ax.set_ylabel("number of builds")
    ax.set_title(f"{data.get('job')} — how much of each build runs without CPU pressure\n"
                 f"(pressured = wait% ≥ {threshold:g}%; {data.get('step')}s step, "
                 f"{days_of(data)}d, n={len(loose)})")
    ax.set_xlim(0, 100)
    ax.legend(fontsize=9)
    ax.grid(axis="y", alpha=0.3)
    fig.tight_layout()
    out = os.path.join(base_dir, "dist_throttle.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  dist_throttle -> {out}")


# --------------------------------------------------------------------------- #
# Renderer 3: time-into-build timelines (timeline_throttle.png, timeline_stall.png)
# --------------------------------------------------------------------------- #
def _envelopes(series_xy, step, min_builds):
    """Bucket all builds' (x_min, y) by minute-into-build; return per-offset p50/p90/max
    at offsets covered by at least `min_builds` builds."""
    buckets = {}
    for xs, ys in series_xy:
        for x, y in zip(xs, ys):
            buckets.setdefault(int(round(x * 60 / step)), []).append(y)
    ks = sorted(k for k in buckets if len(buckets[k]) >= min_builds)
    mins = [k * step / 60 for k in ks]
    p50 = [float(np.percentile(buckets[k], 50)) for k in ks]
    p90 = [float(np.percentile(buckets[k], 90)) for k in ks]
    mx = [float(np.max(buckets[k])) for k in ks]
    return mins, p50, p90, mx


def _draw_population(ax, series_xy, step, min_builds, ylabel, title, cap=None):
    """Faint per-build cloud + across-build median/p90/max envelopes."""
    for xs, ys in series_xy:
        ax.plot(xs, ys, color="#4C78A8", lw=0.5, alpha=0.05)
    mins, p50, p90, mx = _envelopes(series_xy, step, min_builds)
    if mins:
        ax.plot(mins, mx, color="#D62728", lw=2.0, label="max across builds")
        ax.plot(mins, p90, color="#FF7F0E", lw=1.8, ls="--", label="p90 across builds")
        ax.plot(mins, p50, color="#2CA02C", lw=1.8, label="median across builds")
    if cap is not None:
        ax.axhline(cap, color="#8E44AD", ls=":", lw=2, label=f"limit = {cap:g}")
    ax.set_ylabel(ylabel)
    ax.set_title(title)
    ax.legend(fontsize=8, loc="upper right")
    ax.grid(alpha=0.3)


def _draw_samples(ax, samples_xy, ylabel, title, cap=None, sample_labels=None):
    """Only the bold concrete sample builds, on their own clean axes."""
    for i, (xs, ys) in enumerate(samples_xy):
        c = SAMPLE_COLORS[i % len(SAMPLE_COLORS)]
        lbl = sample_labels[i] if sample_labels else None
        ax.plot(xs, ys, color=c, lw=2.0, marker="o", ms=3, label=lbl)
    if cap is not None:
        ax.axhline(cap, color="#8E44AD", ls=":", lw=2, label=f"limit = {cap:g}")
    ax.set_ylabel(ylabel)
    ax.set_title(title)
    ax.legend(fontsize=8, loc="upper right")
    ax.grid(alpha=0.3)


def pick_sample_builds(data, samples=3, seed=None):
    """Choose the concrete sample-build ids highlighted on the timelines ONCE, so the throttle and
    network figures show the *same* runs (otherwise each timeline samples independently and they
    diverge). Candidates are builds present with >= MIN_SAMPLE_POINTS in every timeline series —
    cpu cores + wait, and (when network was fetched) net rx + tx — so a pick renders on every panel.
    Reproducible via `seed`."""
    S = data["series"]
    required = ["cpu_waiting_seconds", "cpu_used_cores"]
    if S.get("net_rx_bytes") and S.get("net_tx_bytes"):
        required += ["net_rx_bytes", "net_tx_bytes"]
    common = set.intersection(*(set(S.get(k, {})) for k in required))
    cands = sorted(b for b in common if all(len(S[k][b]) >= MIN_SAMPLE_POINTS for k in required))
    return random.Random(seed).sample(cands, min(samples, len(cands)))


def plot_throttle_timeline(data, base_dir, picks):
    """Render timeline_throttle.png (PSI "some" = cpu_waiting_seconds) and, when present,
    timeline_stall.png (PSI "full" = cpu_stalled_seconds). Each is a 4-panel figure aligned
    by minutes-into-build: CPU-cores population, pressure-% population, then the same for the
    concrete sample builds in `picks` (chosen by pick_sample_builds so the network timeline
    highlights the identical runs). The same sample builds are reused across both images."""
    S = data["series"]
    step = data.get("step", 30)
    wsrc = S.get("cpu_waiting_seconds")
    if not wsrc:
        print("  timelines: skipped (no cpu_waiting_seconds)")
        return
    csrc = S.get("cpu_used_cores", {})

    # CPU-cores clouds/samples are shared by both figures; only the pressure panels differ.
    cpu_xy = [(x, y) for x, y in (cores(v) for v in csrc.values()) if x]

    cpu_samp, labels = [], []
    for bid in picks:
        cx, cy = cores(csrc[bid])
        wx, wy = wait_pct(wsrc[bid])
        cpu_samp.append((cx, cy))
        dur = cx[-1] if cx else 0
        labels.append(f"{bid[:8]}  ({dur:.0f} min, peak {max(cy):.1f} cores / "
                      f"{max(wy) if wy else 0:.0f}% wait)")

    cap = None
    lp = S.get("cpu_limit_cores", {})
    if lp:
        cap = max((max(v for _, v in pts) for pts in lp.values() if pts), default=None)
    days = days_of(data)

    def render(psrc, metric, out_name):
        """Build the 4-panel figure for one pressure metric (waiting or stalled)."""
        if not psrc:
            print(f"  {out_name}: skipped (no {metric})")
            return
        pres_xy = [(x, y) for x, y in (wait_pct(v) for v in psrc.values()) if x]
        pres_samp = [wait_pct(psrc.get(b, [])) for b in picks]
        n = len(pres_xy)
        word = metric.split("_")[1]                    # waiting | stalled
        ylabel = f"CPU {word} %  ({metric})"

        fig, (ax1, ax2, ax3, ax4) = plt.subplots(4, 1, figsize=(13, 18), sharex=True)
        _draw_population(ax1, cpu_xy, step, MIN_BUILDS_PER_OFFSET, "CPU cores",
                         f"{data.get('job')} — {n} builds overlaid ({step}s, {days}d)", cap=cap)
        _draw_population(ax2, pres_xy, step, MIN_BUILDS_PER_OFFSET, ylabel,
                         f"CPU {word} % — cloud + envelopes")
        _draw_samples(ax3, cpu_samp, "CPU cores",
                      f"CPU cores — {len(picks)} sample builds only", cap=cap, sample_labels=labels)
        _draw_samples(ax4, pres_samp, ylabel,
                      f"CPU {word} % — same sample builds only", sample_labels=[b[:8] for b in picks])
        ax2.set_ylim(0, 100)
        ax4.set_ylim(0, 100)
        for ax in (ax1, ax2, ax3, ax4):  # sharex hides upper tick labels; re-enable, 1 tick/min
            ax.tick_params(labelbottom=True)
            ax.xaxis.set_major_locator(MultipleLocator(1))
            ax.set_xlabel("minutes into build (aligned from each build's start)")
        fig.tight_layout()
        out = os.path.join(base_dir, out_name)
        fig.savefig(out, dpi=130)
        plt.close(fig)
        print(f"  {out_name} -> {out}  ({n} builds, {len(picks)} samples)")

    render(wsrc, "cpu_waiting_seconds", "timeline_throttle.png")
    # timeline_stall.png (PSI "full" = cpu_stalled_seconds) disabled by request: the
    # throttle timeline (PSI "some") already conveys CPU pressure. Re-enable if needed.
    # render(S.get("cpu_stalled_seconds"), "cpu_stalled_seconds", "timeline_stall.png")


def plot_network_timeline(data, base_dir, picks):
    """Render timeline_network.png: the same 4-panel, minutes-into-build layout as
    timeline_throttle.png, but for network throughput instead of CPU. Network "in" (receive)
    takes the role of CPU cores and network "out" (transmit) the role of CPU pressure:

      1. network in  — faint per-build cloud + median/p90/max envelopes across builds;
      2. network out — same population view;
      3. network in  — a few concrete sample builds (red/green/blue, the throttle palette);
      4. network out — the same sample builds.

    Throughput is Δbytes/Δt in MiB/s (see byte_rate). Skips (never raises) when the network
    counters are absent, e.g. data fetched before net_rx_bytes/net_tx_bytes were added."""
    S = data["series"]
    step = data.get("step", 30)
    rxsrc, txsrc = S.get("net_rx_bytes"), S.get("net_tx_bytes")
    if not rxsrc or not txsrc:
        print("  timeline_network: skipped (no net_rx_bytes/net_tx_bytes)")
        return

    rx_xy = [(x, y) for x, y in (byte_rate(v) for v in rxsrc.values()) if x]
    tx_xy = [(x, y) for x, y in (byte_rate(v) for v in txsrc.values()) if x]

    rx_samp, tx_samp, labels = [], [], []
    for bid in picks:
        rx, tx = byte_rate(rxsrc[bid]), byte_rate(txsrc.get(bid, []))
        rx_samp.append(rx)
        tx_samp.append(tx)
        dur = rx[0][-1] if rx[0] else 0
        peak_in = max(rx[1]) if rx[1] else 0
        peak_out = max(tx[1]) if tx[1] else 0
        labels.append(f"{bid[:8]}  ({dur:.0f} min, peak in {peak_in:.1f} / out {peak_out:.1f} MiB/s)")

    n = len(rx_xy)
    days = days_of(data)
    fig, (ax1, ax2, ax3, ax4) = plt.subplots(4, 1, figsize=(13, 18), sharex=True)
    _draw_population(ax1, rx_xy, step, MIN_BUILDS_PER_OFFSET, "network in (MiB/s)",
                     f"{data.get('job')} — {n} builds overlaid ({step}s, {days}d)")
    _draw_population(ax2, tx_xy, step, MIN_BUILDS_PER_OFFSET, "network out (MiB/s)",
                     "network out (transmit) — cloud + envelopes")
    _draw_samples(ax3, rx_samp, "network in (MiB/s)",
                  f"network in (receive) — {len(picks)} sample builds only", sample_labels=labels)
    _draw_samples(ax4, tx_samp, "network out (MiB/s)",
                  "network out (transmit) — same sample builds only",
                  sample_labels=[b[:8] for b in picks])
    for ax in (ax1, ax2, ax3, ax4):  # sharex hides upper tick labels; re-enable, 1 tick/min
        ax.tick_params(labelbottom=True)
        ax.xaxis.set_major_locator(MultipleLocator(1))
        ax.set_xlabel("minutes into build (aligned from each build's start)")
    fig.tight_layout()
    out = os.path.join(base_dir, "timeline_network.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  timeline_network -> {out}  ({n} builds, {len(picks)} samples)")


# --------------------------------------------------------------------------- #
# Entry point
# --------------------------------------------------------------------------- #
def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("path", help="fetch_prow_metrics.py output folder (or its raw_series.json)")
    ap.add_argument("--only", default="all",
                    help=f"comma list of renderers to run: {','.join(RENDERERS)} (default all). "
                         "recommendation.json is written with the 'distribution' renderer.")
    ap.add_argument("--stats", default="mean,peak",
                    help="per-build reductions for the distribution plots (default mean,peak; "
                         "also median, p99, ...)")
    ap.add_argument("--bins", type=int, default=30, help="histogram bins (default 30)")
    ap.add_argument("--min-duration", type=float, default=0.0,
                    help="drop builds shorter than this many minutes (default 0)")
    ap.add_argument("--cpu-algorithm", default=DEFAULT_CPU_ALGORITHM,
                    choices=sorted(CPU_RECOMMENDERS),
                    help="CPU sizing policy for the recommendation (default %(default)s)")
    ap.add_argument("--cpu-target-min", type=float, default=CPU_TARGET_MIN,
                    help="target-duration CPU reco: target build duration in minutes "
                         "(default %(default)s)")
    ap.add_argument("--cpu-legroom-frac", type=float, default=CPU_LEGROOM_FRAC,
                    help="target-duration CPU reco: fractional headroom on top of the p95 "
                         "target (default %(default)s = none; e.g. 0.15 = +15%%)")
    ap.add_argument("--cpu-resolution", type=float, default=CPU_RESOLUTION,
                    help="target-duration CPU reco: round the value up to this core "
                         "granularity (default %(default)s = 100m)")
    ap.add_argument("--cpu-recursive-passes", type=int, default=CPU_RECURSIVE_PASSES,
                    help="target-duration-improved-recursive CPU reco: per-build refinement "
                         "passes (default %(default)s)")
    ap.add_argument("--cpu-max-cores", type=float, default=CPU_MAX_CORES,
                    help="target-duration-improved-recursive CPU reco: hard ceiling in cores "
                         "(default %(default)s = one build node)")
    ap.add_argument("--cpu-min-time-remain-min", type=float, default=CPU_MIN_TIME_REMAIN_MIN,
                    help="target-duration-improved-recursive CPU reco: stop refining a build "
                         "once its leftover target budget drops below this many minutes "
                         "(default %(default)s)")
    ap.add_argument("--threshold", type=float, default=5.0,
                    help="dist_throttle: CPU wait%% at/above this counts as pressured (default 5)")
    ap.add_argument("--samples", type=int, default=3,
                    help="timelines: how many concrete builds to highlight (default 3)")
    ap.add_argument("--seed", type=int, default=None,
                    help="timelines: RNG seed for reproducible sample picks")
    args = ap.parse_args()

    which = set(RENDERERS) if args.only == "all" else {s.strip() for s in args.only.split(",")}
    bad = which - set(RENDERERS)
    if bad:
        ap.error(f"unknown --only value(s): {', '.join(sorted(bad))}; choose from {','.join(RENDERERS)}")

    data, base = load(args.path)
    print(f"{data.get('job')}  ({base})")

    if "distribution" in which:
        cfg = CPURecoConfig(target_min=args.cpu_target_min,
                            legroom_frac=args.cpu_legroom_frac,
                            resolution=args.cpu_resolution,
                            recursive_passes=args.cpu_recursive_passes,
                            max_cores=args.cpu_max_cores,
                            min_time_remain_min=args.cpu_min_time_remain_min)
        reco = generate_recommendation(data, base, args.min_duration, cfg, args.cpu_algorithm)
        stats = [s.strip() for s in args.stats.split(",") if s.strip()]
        plot_distribution(data, base, reco, args.bins, args.min_duration, stats)
        plot_new_cpu_distribution(data, base, reco, args.bins, args.min_duration,
                                  args.cpu_target_min, cfg)
        plot_duration_distribution(data, base, args.bins, args.min_duration,
                                   args.cpu_target_min)
        plot_peak_duration_distribution(data, base, args.bins, args.min_duration, cfg)
        plot_work_distribution(data, base, args.bins, args.min_duration)
        plot_crest_distribution(data, base, args.bins, args.min_duration)
    if "throttle" in which:
        plot_throttle_distribution(data, base, args.threshold, args.bins)
    if "timeline" in which:
        picks = pick_sample_builds(data, args.samples, args.seed)  # shared across both timelines
        plot_throttle_timeline(data, base, picks)
        plot_network_timeline(data, base, picks)


if __name__ == "__main__":
    main()
