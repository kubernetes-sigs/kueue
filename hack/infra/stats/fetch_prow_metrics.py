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
fetch_prow_metrics.py — download CPU/memory build data for a prow job from the
prow "Builds" Grafana/Prometheus backend (https://monitoring-eks.prow.k8s.io).

Configurable:
  1. --job / --job-regex  one exact job, or every kueue job whose name matches a regex
  2. --step               sampling resolution (e.g. 60s, 5m, 30)
  3. date range via --days N  OR  --start / --end (epoch, YYYY-MM-DD, or now-<N><unit>)

Jobs are scoped to org/repo (default kubernetes-sigs/kueue) by the prow:job `repo`
label, so --job-regex only filters *within* kueue's jobs (it is not how we find them).
Matching uses re.search — anchor with ^…$ for an exact match. Use --list-jobs to
preview the match set without downloading.

No third-party deps (stdlib urllib only). Anonymous read access — no token needed.

All output goes under a work directory (--out-dir, default the current dir). Each job
gets its own folder inside it, always named <job>_<range>_<step>[_<suffix>] (only the
optional --suffix is user-controllable) containing:
  raw_series.json        merged per-build time series for all metrics
  per_build_summary.csv  one row per build (peak/mean cpu & mem, duration, req/limit)
  aggregate_stats.json   distribution of per-build peaks across builds
The work directory also holds fetch.log (append-only run log) and, when a batch has
failures, failed_jobs.txt. Re-running resumes: complete jobs are skipped, partial job
folders are cleaned and refetched, so only the gaps are downloaded.

Examples:
  ./fetch_prow_metrics.py --job pull-kueue-test-e2e-baseline-main-1-34 --step 30s --days 30
  ./fetch_prow_metrics.py --job-regex '^pull-.*-main' --list-jobs        # preview only
  ./fetch_prow_metrics.py --job-regex '^periodic-' --days 14 --step 60s  # fetch all periodics
"""
import argparse, csv, json, os, re, shutil, statistics, sys, threading, time, urllib.parse, urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed

# --- prow Grafana / Prometheus backend (see 12750/grafana.md) ---
HOST = "https://monitoring-eks.prow.k8s.io"
DS_UID = "PA553F4D380FC2FA5"                       # "Prometheus Main" datasource
BASE = f"{HOST}/api/datasources/proxy/uid/{DS_UID}/api/v1"
MAX_POINTS = 11000                                 # Prometheus per-series range cap
MIN_CHUNK_S = 3600                                 # don't split a 504'd query window below 1h

# metric key -> recording rule (already carry the id/phase labels)
METRICS = {
    "mem_used_bytes":   "prow:job:memory_working_set_bytes",
    "cpu_used_cores":   "prow:job:cpu_usage_seconds_rate:1m",
    "mem_request_bytes": "prow:job:resource_requests_memory_bytes",
    "cpu_request_cores": "prow:job:resource_requests_cpu_cores",
    "mem_limit_bytes":  "prow:job:resource_limits_memory_bytes",
    "cpu_limit_cores":  "prow:job:resource_limits_cpu_cores",
}
# raw cAdvisor counters (cumulative) — no id label, so joined to prow:job on
# (namespace,pod) to attribute each series to a build.
JOINED_METRICS = {
    # CFS quota accounting -> throttle %
    "cfs_periods":           "container_cpu_cfs_periods_total",
    "cfs_throttled_periods": "container_cpu_cfs_throttled_periods_total",
    "cfs_throttled_seconds": "container_cpu_cfs_throttled_seconds_total",
    # CPU pressure (Linux PSI): cumulative seconds tasks were runnable but couldn't get a
    # CPU = unmet CPU demand. rate() of these = fraction of time under CPU pressure.
    "cpu_waiting_seconds":   "container_pressure_cpu_waiting_seconds_total",   # ≥1 task waiting ("some")
    "cpu_stalled_seconds":   "container_pressure_cpu_stalled_seconds_total",   # all tasks stalled ("full")
    # OOM-kill counter: a build whose value increases hit its memory limit, so its peak
    # sits at the ceiling and must be excluded from the memory recommendation.
    "oom_events":            "container_oom_events_total",
}
GIB = 1024 ** 3


def parse_step(s):
    """'60s' / '5m' / '2h' / '30' -> seconds (int)."""
    s = str(s).strip()
    units = {"s": 1, "m": 60, "h": 3600, "d": 86400}
    if s and s[-1] in units:
        return int(float(s[:-1]) * units[s[-1]])
    return int(s)


def parse_time(s, now):
    """epoch int | 'now' | 'now-<N><unit>' | 'YYYY-MM-DD' | 'YYYY-MM-DDTHH:MM:SS' -> epoch int."""
    s = str(s).strip()
    if s == "now":
        return now
    if s.startswith("now-"):
        return now - parse_step(s[4:])
    if s.isdigit():
        return int(s)
    for fmt in ("%Y-%m-%dT%H:%M:%S", "%Y-%m-%d"):
        try:
            return int(time.mktime(time.strptime(s, fmt)))
        except ValueError:
            pass
    raise ValueError(f"cannot parse time: {s!r}")


def http_get(path, params, retries=5):
    """GET with exponential backoff on transient errors (403 rate-limit, 5xx, timeouts).
    The anonymous prow proxy throttles sustained querying with 403s; a short wait clears it.
    504 (Gateway Timeout) is NOT retried here — it means the query is too expensive for the
    proxy, so it is raised for query_range_chunked to react to by splitting the time window."""
    url = f"{BASE}/{path}?" + urllib.parse.urlencode(params)
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(url, timeout=90) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            if e.code not in (403, 429, 500, 502, 503) or attempt == retries - 1:
                raise
            reason = f"HTTP {e.code} {e.reason}"
        except (urllib.error.URLError, TimeoutError) as e:
            if attempt == retries - 1:
                raise
            reason = str(getattr(e, "reason", "") or e) or type(e).__name__
        wait = 5 * (2 ** attempt)  # 5,10,20,40,80s
        print(f"  retry {attempt+1}/{retries} after {wait}s ({reason})", file=sys.stderr)
        time.sleep(wait)


def query_range_chunked(expr, start, end, step):
    """Run query_range in <=MAX_POINTS windows; return merged {id: {ts: val}}.
    A window whose query is too expensive for the proxy (504 Gateway Timeout) is split in
    half and retried, recursively down to MIN_CHUNK_S, so heavy joins still complete as
    several lighter queries rather than failing the whole fetch."""
    merged = {}
    chunk = (MAX_POINTS - 100) * step
    s = start
    while s < end:
        e = min(s + chunk, end)
        _fetch_window(expr, s, e, step, merged)
        s = e
    return merged


def _fetch_window(expr, s, e, step, merged):
    """Fetch [s, e] into `merged`; on a 504 (query too expensive for the proxy) split the
    window in half and recurse."""
    try:
        res = http_get("query_range", {"query": expr, "start": s, "end": e, "step": step})
    except urllib.error.HTTPError as err:
        if err.code == 504 and (e - s) > MIN_CHUNK_S:
            mid = (s + e) // 2
            print(f"  HTTP 504 on {(e - s) // 3600}h window; splitting in half", file=sys.stderr)
            _fetch_window(expr, s, mid, step, merged)
            _fetch_window(expr, mid, e, step, merged)
            return
        raise
    if res.get("status") != "success":
        print(f"  WARN window [{s},{e}] failed: {res.get('error')}", file=sys.stderr)
        return
    for ser in res["data"]["result"]:
        bid = ser["metric"].get("id", "?")
        d = merged.setdefault(bid, {})
        for t, v in ser["values"]:
            d[float(t)] = float(v)


def completed_build_ids(org, repo, job, terminal, start, end):
    """Build ids that reached a terminal phase (Succeeded/Failed) = real, non-cancelled runs."""
    sel = f'prow:job{{org="{org}",repo="{repo}",name="{job}",phase=~"{terminal}"}}'
    res = http_get("series", {"match[]": sel, "start": start, "end": end})
    return {s.get("id") for s in res.get("data", [])}


def list_jobs(org, repo, start, end):
    """All distinct prow job names for org/repo over [start,end], from the series index.
    Scoping is by the prow:job `repo` label, so this returns exactly that repo's jobs."""
    sel = f'prow:job{{org="{org}",repo="{repo}"}}'
    res = http_get("series", {"match[]": sel, "start": start, "end": end})
    return sorted({s.get("name") for s in res.get("data", []) if s.get("name")})


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sel = ap.add_mutually_exclusive_group(required=True)
    sel.add_argument("--job", help="exact prow job name to download "
                                    "(e.g. pull-kueue-test-e2e-baseline-main-1-34)")
    sel.add_argument("--job-regex", dest="job_regex",
                     help="download every kueue job whose name matches this Python regex "
                          "(re.search; anchor with ^…$ for exact). Scoped to --org/--repo.")
    ap.add_argument("--list-jobs", "-n", "--dry-run", action="store_true", dest="list_jobs",
                    help="print the selected job name(s) and exit without downloading")
    ap.add_argument("--step", default="60s", help="sampling resolution: 60s, 5m, 30 (default 60s)")
    ap.add_argument("--days", type=float, help="range = last N days (relative to now)")
    ap.add_argument("--start", help="range start: epoch | YYYY-MM-DD | now-7d (default now-7d)")
    ap.add_argument("--end", help="range end: epoch | YYYY-MM-DD | now (default now)")
    ap.add_argument("--repo", default="kueue")
    ap.add_argument("--org", default="kubernetes-sigs")
    ap.add_argument("--phase", default="Running", help='phase whose usage samples to measure (default "Running")')
    ap.add_argument("--terminal-phases", default="Succeeded|Failed",
                    help='builds counted as real must reach one of these phases (default "Succeeded|Failed"); '
                         "cancelled/superseded PR builds never do and are dropped")
    ap.add_argument("--include-cancelled", action="store_true",
                    help="keep builds that never reached a terminal phase (off by default)")
    ap.add_argument("--min-duration", default="0",
                    help="also drop builds whose observed sample span is shorter than this "
                         "(e.g. 90s, 2m; bare number = seconds). Catches compile-error builds that "
                         "terminate before the workload runs. Resolution is bounded by --step: at 60s "
                         "sampling a build with <2 samples reads as 0s, so ~1 step is the effective floor.")
    ap.add_argument("--suffix", default=None,
                    help="optional suffix appended to each job's folder name (e.g. 'clean')")
    ap.add_argument("--out-dir", default=".", metavar="DIR",
                    help="work directory that holds all per-job folders, fetch.log and "
                         "failed_jobs.txt (default current dir). Each job folder inside it is "
                         "always named <job>_<range>_<step>[_<suffix>]; only --suffix changes it.")
    ap.add_argument("--concurrency", type=int, default=1, metavar="N",
                    help="fetch N jobs in parallel (default 1 = sequential). Faster, but "
                         "pressures the anonymous proxy's rate limit; per-request backoff absorbs 403s.")
    ap.add_argument("--sleep", type=float, default=0, metavar="SECONDS",
                    help="pause between starting jobs to ease off the proxy rate limiter "
                         "(default 0; refetch_failed.sh used 15).")
    ap.add_argument("--retries", type=int, default=0, metavar="N",
                    help="after the batch, retry still-failed jobs up to N more passes "
                         "(default 0). Complements the per-request backoff in http_get.")
    ap.add_argument("--force", action="store_true",
                    help="re-fetch even jobs whose output folder is already complete; "
                         "by default completed jobs are skipped, so re-running only fills gaps.")
    args = ap.parse_args()
    if args.concurrency < 1:
        ap.error("--concurrency must be >= 1")

    now = int(time.time())
    if args.days is not None:
        start, end = now - int(args.days * 86400), now
    else:
        end = parse_time(args.end or "now", now)
        start = parse_time(args.start or "now-7d", now)
    step = parse_step(args.step)
    if start >= end:
        ap.error("start must be before end")

    # resolve the job list: one exact --job, or every kueue job matching --job-regex
    if args.job:
        jobs = [args.job]
    else:
        try:
            pat = re.compile(args.job_regex)
        except re.error as e:
            ap.error(f"invalid --job-regex: {e}")
        allj = list_jobs(args.org, args.repo, start, end)
        jobs = [j for j in allj if pat.search(j)]
        if not jobs:
            print(f"no {args.repo} jobs match /{args.job_regex}/ "
                  f"(of {len(allj)} in range); try --list-jobs with a broader pattern.",
                  file=sys.stderr)
            return
    if args.list_jobs:
        print(f"{len(jobs)} job(s) selected:")
        for j in jobs:
            print(" ", j)
        return

    run_batch(jobs, start, end, step, args)


def out_dir_for(job, step, args):
    """Folder a job's output goes to: <work-dir>/<job>_<range>_<step>[_<suffix>].
    The folder name is always derived from job/range/step; only --suffix can extend it."""
    name = f"{job}_{round(args._range_days)}d_{step}s"
    if args.suffix:
        name += f"_{args.suffix}"
    return os.path.join(args.out_dir, name)


def is_complete(job, step, args):
    """A job is complete when its last-written file (aggregate_stats.json) exists."""
    return os.path.exists(os.path.join(out_dir_for(job, step, args), "aggregate_stats.json"))


def run_pass(jobs, start, end, step, args, verbose, logb):
    """Fetch one pass over `jobs`. Returns (ok_jobs, failed_jobs)."""
    ok, failed = [], []
    total = len(jobs)
    lock = threading.Lock()
    done = 0

    def work(job):
        try:
            return job, fetch_one(job, start, end, step, args, verbose=verbose)
        except Exception as e:  # keep the batch going
            return job, e

    def record(job, res):
        nonlocal done
        done += 1
        if isinstance(res, Exception):
            failed.append(job)
            logb(f"  [{done}/{total}] !! {job}: {res}")
        elif res:
            ok.append(job)
            if not verbose:
                logb(f"  [{done}/{total}] ok  {job}  ({res} builds)")
        else:
            failed.append(job)
            if not verbose:
                logb(f"  [{done}/{total}] -- {job}  (no builds)")

    if args.concurrency <= 1:
        for job in jobs:
            j, res = work(job)
            record(j, res)
            if args.sleep and job is not jobs[-1]:
                time.sleep(args.sleep)
    else:
        with ThreadPoolExecutor(max_workers=args.concurrency) as ex:
            futs = []
            for job in jobs:
                futs.append(ex.submit(work, job))
                if args.sleep:
                    time.sleep(args.sleep)  # stagger submissions to spread proxy load
            for fut in as_completed(futs):
                j, res = fut.result()
                with lock:
                    record(j, res)
    return ok, failed


def run_batch(jobs, start, end, step, args):
    """Fetch every job into the work dir: skip completed jobs, clean partial folders,
    log progress to <work>/fetch.log, retry failures, and record leftovers in
    <work>/failed_jobs.txt."""
    args._range_days = (end - start) / 86400
    os.makedirs(args.out_dir, exist_ok=True)
    with open(os.path.join(args.out_dir, "fetch.log"), "a") as logf:
        def logb(msg=""):
            print(msg)
            logf.write(msg + "\n")
            logf.flush()

        logb(f"\n===== run {time.strftime('%Y-%m-%d %H:%M:%S')} — {len(jobs)} job(s), "
             f"{round(args._range_days)}d/{step}s, concurrency {args.concurrency} =====")
        logb(f"jobs to fetch ({len(jobs)}):")
        for j in jobs:
            logb(f"  - {j}")

        pending = jobs if args.force else [j for j in jobs if not is_complete(j, step, args)]
        skipped = len(jobs) - len(pending)
        if skipped:
            logb(f"skipping {skipped} already-complete job(s) (use --force to re-fetch)")

        # error cleaning: drop any leftover partial folders before refetching them
        for j in pending:
            d = out_dir_for(j, step, args)
            if os.path.isdir(d) and not is_complete(j, step, args):
                shutil.rmtree(d, ignore_errors=True)
                logb(f"  cleaned partial folder: {d}")

        if not pending:
            logb("nothing to fetch.")
            return

        verbose = len(jobs) == 1 and args.concurrency == 1
        all_ok, failed = [], pending
        for attempt in range(args.retries + 1):
            if attempt:
                logb(f"\n=== retry pass {attempt}/{args.retries}: {len(failed)} job(s) ===")
                if args.sleep:
                    time.sleep(args.sleep)
            ok, failed = run_pass(failed, start, end, step, args, verbose, logb)
            all_ok += ok
            if not failed:
                break

        # record leftovers so the user can see (and rerun to fetch) what still failed
        failed_path = os.path.join(args.out_dir, "failed_jobs.txt")
        if failed:
            with open(failed_path, "w") as f:
                f.write("\n".join(failed) + "\n")
        elif os.path.exists(failed_path):
            os.remove(failed_path)

        if len(jobs) > 1 or failed:
            logb(f"\n=== done: {len(all_ok)} ok, {len(failed)} failed ===")
            for j in failed:
                logb(f"  failed: {j}")
            if failed:
                logb(f"  (listed in {failed_path}; rerun the same command to fetch them)")


def fetch_one(job, start, end, step, args, verbose=True):
    """Download + summarize one job into its own folder. Returns the number of builds written."""
    log = print if verbose else (lambda *a, **k: None)
    npts = (end - start) // step
    out_dir = out_dir_for(job, step, args)
    os.makedirs(out_dir, exist_ok=True)

    def ts(x):
        return time.strftime("%Y-%m-%d %H:%M", time.localtime(x))
    log(f"job    : {job}")
    log(f"range  : {ts(start)} .. {ts(end)}  ({(end-start)/86400:.1f}d)")
    log(f"step   : {step}s  (~{npts} pts/series; chunked at {MAX_POINTS})")
    log(f"filter : org={args.org} repo={args.repo} phase={args.phase}")
    log(f"out    : {out_dir}\n")

    sel = f'{{org="{args.org}",repo="{args.repo}",name="{job}",phase="{args.phase}"}}'
    data = {}
    for key, rule in METRICS.items():
        data[key] = query_range_chunked(f"sum by (id)({rule}{sel})", start, end, step)
        log(f"  {key:20} {len(data[key])} builds")
    # cAdvisor counters: join to prow:job on (namespace,pod) to attach the id label.
    # prow:job sometimes has two series for the same (namespace,pod) — one plain and one
    # carrying extra node/pod_ip labels — which makes group_left a many-to-many match and
    # returns 422. max by (namespace,pod,id) collapses them to one series per pod first.
    for key, cadv in JOINED_METRICS.items():
        expr = (f'sum by (id)({cadv}{{container="test"}} * on(namespace,pod) '
                f'group_left(id) max by (namespace,pod,id)(prow:job{sel}))')
        data[key] = query_range_chunked(expr, start, end, step)
        log(f"  {key:20} {len(data[key])} builds")

    # --- clean: drop cancelled/superseded builds (never reached a terminal phase) ---
    all_ids = set().union(*(set(v) for v in data.values())) if data else set()
    if not args.include_cancelled:
        keep = completed_build_ids(args.org, args.repo, job, args.terminal_phases, start, end)
        dropped = sorted(all_ids - keep)
        for key in data:
            data[key] = {b: v for b, v in data[key].items() if b in keep}
        log(f"\n  cleaned: dropped {len(dropped)} cancelled/incomplete builds "
            f"(no {args.terminal_phases} phase); kept {len(data['mem_used_bytes'])}")

    # --- clean: optional minimum-duration filter (seconds) ---
    min_dur_s = parse_step(args.min_duration)
    if min_dur_s > 0:
        if min_dur_s < step:
            log(f"  note: --min-duration {min_dur_s}s is below --step {step}s; "
                f"effectively drops only builds with <2 samples (span 0).")
        short = set()
        for b, pts in data["mem_used_bytes"].items():
            ts = list(pts)
            if len(ts) < 2 or (max(ts) - min(ts)) < min_dur_s:
                short.add(b)
        for key in data:
            data[key] = {b: v for b, v in data[key].items() if b not in short}
        log(f"  cleaned: dropped {len(short)} builds shorter than {min_dur_s}s; "
            f"kept {len(data['mem_used_bytes'])}")

    # --- raw dump ---
    raw = {"job": job, "org": args.org, "repo": args.repo, "phase": args.phase,
           "start": start, "end": end, "step": step,
           "series": {k: {b: sorted(d.items()) for b, d in v.items()} for k, v in data.items()}}
    with open(os.path.join(out_dir, "raw_series.json"), "w") as f:
        json.dump(raw, f)

    # --- per-build summary ---
    def peak(metric, bid):
        v = list(data[metric].get(bid, {}).values())
        return max(v) if v else float("nan")

    rows = []
    for bid in sorted(data["mem_used_bytes"]):
        mt = sorted(data["mem_used_bytes"][bid])
        mv = [data["mem_used_bytes"][bid][t] for t in mt]
        cv = list(data["cpu_used_cores"].get(bid, {}).values())
        dur = (mt[-1] - mt[0]) / 60 if len(mt) > 1 else 0
        rows.append({
            "build_id": bid, "samples": len(mv), "duration_min": round(dur, 1),
            "mem_peak_gib": round(max(mv) / GIB, 3) if mv else 0,
            "mem_mean_gib": round(statistics.mean(mv) / GIB, 3) if mv else 0,
            "cpu_peak_cores": round(max(cv), 3) if cv else 0,
            "cpu_mean_cores": round(statistics.mean(cv), 3) if cv else 0,
            "req_mem_gib": round(peak("mem_request_bytes", bid) / GIB, 3),
            "lim_mem_gib": round(peak("mem_limit_bytes", bid) / GIB, 3),
            "req_cpu_cores": round(peak("cpu_request_cores", bid), 3),
            "lim_cpu_cores": round(peak("cpu_limit_cores", bid), 3),
        })

    if not rows:
        log("\nNo builds found for that job/range/phase. Check the job name with:")
        log(f'  curl -s "{BASE}/label/name/values" | grep {args.repo}')
        return 0

    with open(os.path.join(out_dir, "per_build_summary.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=list(rows[0].keys()))
        w.writeheader()
        w.writerows(rows)

    def agg(col):
        v = sorted(r[col] for r in rows if r[col] == r[col])
        if not v:
            return {}
        Q = statistics.quantiles(v, n=20) if len(v) >= 2 else [v[0]] * 19
        return {"n": len(v), "min": round(min(v), 3), "p50": round(statistics.median(v), 3),
                "p90": round(Q[17], 3), "p95": round(Q[18], 3),
                "max": round(max(v), 3), "mean": round(statistics.mean(v), 3)}

    summary = {"job": job, "range_days": round((end - start) / 86400, 2),
               "step_s": step, "builds": len(rows),
               "mem_peak_gib": agg("mem_peak_gib"), "mem_mean_gib": agg("mem_mean_gib"),
               "cpu_peak_cores": agg("cpu_peak_cores"), "cpu_mean_cores": agg("cpu_mean_cores"),
               "duration_min": agg("duration_min"),
               "configured": {k: rows[0][k] for k in ("req_mem_gib", "lim_mem_gib", "req_cpu_cores", "lim_cpu_cores")}}
    with open(os.path.join(out_dir, "aggregate_stats.json"), "w") as f:
        json.dump(summary, f, indent=2)

    log(f"\n{len(rows)} builds -> {out_dir}/")
    log(f"{'build_id':38}{'dur':>6}{'memPk':>8}{'memMn':>8}{'cpuPk':>8}{'cpuMn':>8}")
    for r in rows:
        log(f"{r['build_id']:38}{r['duration_min']:6.1f}{r['mem_peak_gib']:8.2f}"
            f"{r['mem_mean_gib']:8.2f}{r['cpu_peak_cores']:8.2f}{r['cpu_mean_cores']:8.2f}")
    log("\naggregate_stats.json:")
    log(json.dumps(summary, indent=2))
    return len(rows)


if __name__ == "__main__":
    main()
