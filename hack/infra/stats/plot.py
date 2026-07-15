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
  dist_work.png        per-build CPU work histogram (avg CPU × duration, core-minutes)
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

# Work-conserving CPU right-sizing (issue #12750). Assume each build's CPU work
# (avg cores x duration) is roughly invariant to the request: CPU is compressible, so
# fewer cores just stretch the build. A build given `work / target` cores would then
# finish in about `target` minutes. These are the defaults; each is overridable via CLI.
CPU_TARGET_MIN = 7.5  # target build duration in minutes to size the request against
CPU_LEGROOM_FRAC = 0.15  # fractional headroom on top of the p95 target (0.15 = +15%)
CPU_RESOLUTION = 0.1   # round the recommendation up to this core granularity (100m)

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
def compute_cpu_target(data, min_dur, target_min, legroom_frac, resolution):
    """Work-conserving CPU recommendation: the single Guaranteed-QoS value (request ==
    limit, as test-infra requires) that would let builds finish in about target_min
    minutes. Sizes off p95 of the per-build target mean CPU (see target_new_avg_cpus), adds
    fractional legroom (e.g. 0.15 = +15%), rounds up to `resolution`, and caps at the
    current limit (never
    recommend more than is already allocated). Returns (value, stats) or (None, None) when
    there is too little CPU data. `stats` carries the percentiles that justify the value."""
    oom = oom_build_ids(data)
    pairs = per_build_cpu_work(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    if len(pairs) < 2:
        return None, None
    new = target_new_avg_cpus(pairs, target_min)
    durs = np.array([d for _, d in pairs])
    p95 = float(np.percentile(new, 95))
    val = round_up_to(p95 * (1 + legroom_frac), resolution)
    cpu_lim_cur = const(data, "cpu_limit_cores")
    if cpu_lim_cur is not None:
        val = min(val, cpu_lim_cur)
    stats = {
        "target_min": target_min,
        "legroom_frac": legroom_frac,
        "resolution": resolution,
        "new_mean_p50": round(float(np.percentile(new, 50)), 3),
        "new_mean_p95": round(p95, 3),
        "new_mean_p99": round(float(np.percentile(new, 99)), 3),
        "duration_p50_min": round(float(np.percentile(durs, 50)), 2),
        "duration_p95_min": round(float(np.percentile(durs, 95)), 2),
    }
    return val, stats


def compute_reco(data, min_dur, target_min=CPU_TARGET_MIN, legroom_frac=CPU_LEGROOM_FRAC,
                 resolution=CPU_RESOLUTION):
    """Recommended request/limit from the usage distribution:
      cpu request = p95 of per-build mean x1.15, capped at the current limit;
      cpu limit   = kept as-is (throttling only; measured peaks are rate() artifacts);
      cpu target  = work-conserving Guaranteed-QoS value (see compute_cpu_target), a
        fractional alternative that trades runtime for cores on fast jobs;
      mem request == mem limit = largest observed per-build peak x1.15. Memory is
        incompressible, so the value must hold the worst peak. Builds that OOM-killed are
        excluded first (their peak sits pinned at the ceiling), so the max is taken over
        healthy builds only and is not polluted by those artifacts.
    Returns the reco dict, or None if the job has too little CPU/memory data to size."""
    oom = oom_build_ids(data)

    def xp(key, stat, p, gib):
        vals = per_build(data["series"].get(key, {}), stat, min_dur, exclude=oom)
        if gib:
            vals = vals / GIB
        return float(np.percentile(vals, p)) if len(vals) else None

    cpu_mean_p95 = xp("cpu_used_cores", "mean", 95, False)
    mem_peak_max = xp("mem_used_bytes", "peak", 100, True)
    if cpu_mean_p95 is None or mem_peak_max is None:
        return None

    cpu_lim_cur = const(data, "cpu_limit_cores")
    cpu_req_raw = math.ceil(cpu_mean_p95 * 1.15)
    cpu_lim = int(cpu_lim_cur) if cpu_lim_cur else cpu_req_raw
    mem_val = math.ceil(mem_peak_max * 1.15)
    cpu_target, cpu_target_stats = compute_cpu_target(data, min_dur, target_min, legroom_frac, resolution)
    return {
        "cpu_req": min(cpu_req_raw, cpu_lim),
        "cpu_lim": cpu_lim,
        "cpu_target": cpu_target,
        "cpu_target_stats": cpu_target_stats,
        "mem_req": mem_val,
        "mem_lim": mem_val,
        "saturated": cpu_lim_cur is not None and cpu_req_raw >= cpu_lim_cur,
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
    return {
        "job": data.get("job"),
        "range_days": round((data["end"] - data["start"]) / 86400, 2),
        "step_s": data.get("step"),
        "builds": n,
        "builds_oom_excluded": len(oom),
        "cpu": {
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
            "request_target_based": reco.get("cpu_target"),
            "saved_target_based": saved(cpu_req_cur, reco.get("cpu_target")),
            "target": reco.get("cpu_target_stats"),
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


def generate_recommendation(data, base_dir, min_dur, target_min=CPU_TARGET_MIN,
                            legroom_frac=CPU_LEGROOM_FRAC, resolution=CPU_RESOLUTION):
    """Compute the sizing recommendation and write recommendation.json. Returns the reco
    dict (or None, printing a skip note, when the job has too little data)."""
    reco = compute_reco(data, min_dur, target_min, legroom_frac, resolution)
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
                              target_min=CPU_TARGET_MIN):
    """Render dist_mean_new_cpu.png: three stacked panels.
      top    — each build's CURRENT mean CPU (today's demand);
      middle — each build's TARGET mean CPU = work / max(duration, target_min), i.e. the
               request that would stretch the build to ~target_min minutes at constant work;
      bottom — the per-build DURATION distribution (minutes), with the target marked, which
               explains the shift above: builds already at/above the target are left alone.
    The top two panels share x and y so the leftward CPU shift is obvious; the duration
    panel is on its own minutes axis. The middle panel's p50/p95/p99 justify the gold
    recommended request (reco['cpu_target']). OOM-killed builds are excluded throughout."""
    oom = oom_build_ids(data)
    pairs = per_build_cpu_work(data["series"].get("cpu_used_cores", {}), min_dur, exclude=oom)
    if len(pairs) < 2:
        print("  dist_mean_new_cpu: skipped (not enough CPU data)")
        return
    old = np.array([avg for avg, _ in pairs])
    new = target_new_avg_cpus(pairs, target_min)
    durs = np.array([d for _, d in pairs])
    cpu_req_cur, cpu_lim_cur = const(data, "cpu_request_cores"), const(data, "cpu_limit_cores")
    rec = (reco or {}).get("cpu_target")

    # Top two panels share bin edges + x/y so their geometry is identical and comparable;
    # the duration panel (bottom) lives on its own minutes axis, so it is not shared.
    xmax = max(old.max(), float(new.max()), cpu_req_cur or 0.0) * 1.05 or 1.0
    edges = np.linspace(0, xmax, bins + 1)
    fig = plt.figure(figsize=(11, 5.4 * 3))
    ax_cur = fig.add_subplot(3, 1, 1)
    ax_tgt = fig.add_subplot(3, 1, 2, sharex=ax_cur, sharey=ax_cur)
    ax_dur = fig.add_subplot(3, 1, 3)
    _hist_on_ax(ax_cur, old, "per-build average CPU cores",
                f"current — distribution across {len(old)} builds of each build's average CPU",
                bins=edges, req=cpu_req_cur, lim=cpu_lim_cur)
    _hist_on_ax(ax_tgt, new, "per-build target-duration CPU cores",
                f"target {target_min:g} min — each build's work / max(duration, {target_min:g} min)",
                bins=edges, req=cpu_req_cur, rec_req=rec, rec_lim=rec)
    ax_cur.set_xlim(0, xmax)

    _draw_duration_hist(ax_dur, durs, target_min, bins)

    fig.suptitle(f"{data.get('job')}  —  current vs {target_min:g}-min-target mean CPU  —  "
                 f"{data.get('step')}s step, {days_of(data)}d", fontsize=13, y=0.995)
    fig.tight_layout(rect=[0, 0, 1, 0.99])
    out = os.path.join(base_dir, "dist_mean_new_cpu.png")
    fig.savefig(out, dpi=130)
    plt.close(fig)
    print(f"  dist_mean_new_cpu -> {out}"
          + (f"  (rec target request = {rec:g} cores)" if rec is not None else ""))


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
    ap.add_argument("--cpu-target-min", type=float, default=CPU_TARGET_MIN,
                    help="work-conserving CPU reco: target build duration in minutes "
                         "(default %(default)s)")
    ap.add_argument("--cpu-legroom-frac", type=float, default=CPU_LEGROOM_FRAC,
                    help="work-conserving CPU reco: fractional headroom on top of the p95 "
                         "target (default %(default)s = +15%%)")
    ap.add_argument("--cpu-resolution", type=float, default=CPU_RESOLUTION,
                    help="work-conserving CPU reco: round the value up to this core "
                         "granularity (default %(default)s = 100m)")
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
        reco = generate_recommendation(data, base, args.min_duration, args.cpu_target_min,
                                       args.cpu_legroom_frac, args.cpu_resolution)
        stats = [s.strip() for s in args.stats.split(",") if s.strip()]
        plot_distribution(data, base, reco, args.bins, args.min_duration, stats)
        plot_new_cpu_distribution(data, base, reco, args.bins, args.min_duration,
                                  args.cpu_target_min)
        plot_duration_distribution(data, base, args.bins, args.min_duration,
                                   args.cpu_target_min)
        plot_work_distribution(data, base, args.bins, args.min_duration)
    if "throttle" in which:
        plot_throttle_distribution(data, base, args.threshold, args.bins)
    if "timeline" in which:
        picks = pick_sample_builds(data, args.samples, args.seed)  # shared across both timelines
        plot_throttle_timeline(data, base, picks)
        plot_network_timeline(data, base, picks)


if __name__ == "__main__":
    main()
