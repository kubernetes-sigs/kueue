#!/usr/bin/env python3
"""
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
"""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import re
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Iterable

DEFAULT_BUCKET = "kubernetes-ci-logs"
KUEUE_PERIODICS_URL = (
    "https://raw.githubusercontent.com/kubernetes/test-infra/master/config/jobs/kubernetes-sigs/kueue/kueue-periodics-main.yaml"
)

@dataclass(frozen=True)
class BuildRuntime:
    job: str
    build_id: str
    result: str
    start: dt.datetime
    finish: dt.datetime
    duration_seconds: int
    prow_url: str

def http_get(url: str, *, tries: int = 4, timeout: int = 30) -> bytes:
    last_exc: Exception | None = None
    for attempt in range(tries):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "kueue-prow-runtime/1.0"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.read()
        except urllib.error.HTTPError as e:
            if e.code == 404:
                raise
            last_exc = e
        except urllib.error.URLError as e:
            last_exc = e
        time.sleep(0.5 * (2**attempt))
    raise last_exc  # type: ignore[misc]

def http_json(url: str) -> dict[str, Any]:
    return json.loads(http_get(url).decode("utf-8"))

def parse_gs_prefix(job_or_prefix: str, bucket: str = DEFAULT_BUCKET) -> tuple[str, str, str]:
    value = job_or_prefix.strip().rstrip("/")

    if value.startswith("gs://"):
        rest = value.removeprefix("gs://")
        bucket_name, _, path = rest.partition("/")
        return path.rstrip("/").split("/")[-1], bucket_name, path.rstrip("/")

    if value.startswith("https://storage.googleapis.com/"):
        rest = value.removeprefix("https://storage.googleapis.com/").rstrip("/")
        bucket_name, _, path = rest.partition("/")
        return path.rstrip("/").split("/")[-1], bucket_name, path.rstrip("/")

    if value.startswith("logs/") or value.startswith("pr-logs/"):
        return value.rstrip("/").split("/")[-1], bucket, value.rstrip("/")

    return value, bucket, f"logs/{value}"

def object_url(bucket: str, path: str) -> str:
    return f"https://storage.googleapis.com/{bucket}/{urllib.parse.quote(path, safe='/')}"

def list_build_ids(bucket: str, prefix: str) -> list[str]:
    ids: list[str] = []
    page_token: str | None = None

    while True:
        params = {
            "prefix": f"{prefix.rstrip('/')}/",
            "delimiter": "/",
            "fields": "nextPageToken,prefixes",
        }
        if page_token:
            params["pageToken"] = page_token

        url = f"https://storage.googleapis.com/storage/v1/b/{bucket}/o?{urllib.parse.urlencode(params)}"
        data = http_json(url)

        for common_prefix in data.get("prefixes", []):
            build_id = common_prefix.rstrip("/").split("/")[-1]
            if build_id.isdigit():
                ids.append(build_id)

        page_token = data.get("nextPageToken")
        if not page_token:
            break

    return sorted(set(ids), key=int, reverse=True)

def parse_ts(value: Any) -> dt.datetime:
    if isinstance(value, (int, float)):
        return dt.datetime.fromtimestamp(float(value), tz=dt.timezone.utc)

    text = str(value).strip()
    if re.fullmatch(r"\d+(\.\d+)?", text):
        return dt.datetime.fromtimestamp(float(text), tz=dt.timezone.utc)

    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    parsed = dt.datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed.astimezone(dt.timezone.utc)

def first_present(d: dict[str, Any], keys: Iterable[str]) -> Any:
    for key in keys:
        if key in d and d[key] not in (None, ""):
            return d[key]
    raise KeyError(f"none of these keys found: {list(keys)}")

def read_build_runtime(job: str, bucket: str, prefix: str, build_id: str) -> BuildRuntime | None:
    base = f"{prefix.rstrip('/')}/{build_id}"
    try:
        started = http_json(object_url(bucket, f"{base}/started.json"))
        finished = http_json(object_url(bucket, f"{base}/finished.json"))
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return None
        raise

    start = parse_ts(first_present(started, ["timestamp", "startTime", "started"]))
    finish = parse_ts(first_present(finished, ["timestamp", "completionTime", "finished"]))
    seconds = int(round((finish - start).total_seconds()))
    if seconds < 0:
        raise ValueError(f"negative duration for {job} build {build_id}: {seconds}s")

    result = str(finished.get("result") or finished.get("passed") or "unknown")
    return BuildRuntime(
        job=job,
        build_id=build_id,
        result=result,
        start=start,
        finish=finish,
        duration_seconds=seconds,
        prow_url=f"https://prow.k8s.io/view/gs/{bucket}/{base}",
    )

def last_n_builds(
    job_or_prefix: str,
    *,
    limit: int,
    bucket: str = DEFAULT_BUCKET,
    only_success: bool = False,
    max_to_check: int = 200,
) -> list[BuildRuntime]:
    job, bucket, prefix = parse_gs_prefix(job_or_prefix, bucket=bucket)
    out: list[BuildRuntime] = []

    for build_id in list_build_ids(bucket, prefix)[:max_to_check]:
        build = read_build_runtime(job, bucket, prefix, build_id)
        if build is None:
            continue
        if only_success and build.result not in {"SUCCESS", "success", "true", "True"}:
            continue
        out.append(build)
        if len(out) >= limit:
            break

    return out
def load_kueue_periodic_jobs(*, verbose: bool = False) -> list[str]:
    text = http_get(KUEUE_PERIODICS_URL).decode("utf-8")
    if verbose:
        print(text, file=sys.stderr)
    names = re.findall(r"(?m)^\s*-?\s+name:\s+(periodic-kueue[-A-Za-z0-9_.]+)\s*$", text)
    return sorted(dict.fromkeys(names))
def fmt_duration(seconds: float | int) -> str:
    seconds = int(round(seconds))
    h, rem = divmod(seconds, 3600)
    m, s = divmod(rem, 60)
    return f"{h}:{m:02d}:{s:02d}" if h else f"{m}:{s:02d}"

def summarize(job: str, builds: list[BuildRuntime]) -> dict[str, Any]:
    durations = [b.duration_seconds for b in builds]
    return {
        "job": job,
        "build_count": len(builds),
        "avg_seconds": statistics.mean(durations) if durations else None,
        "avg": fmt_duration(statistics.mean(durations)) if durations else "N/A",
        "runtimes": [fmt_duration(x) for x in durations],
        "build_ids": [b.build_id for b in builds],
        "results": [b.result for b in builds],
        "prow_urls": [b.prow_url for b in builds],
    }

def print_markdown(rows: list[dict[str, Any]]) -> None:
    print("| Rank | Job | Avg | Last runtimes | Build IDs | Results |")
    print("|---:|---|---:|---|---|---|")
    for i, row in enumerate(rows, 1):
        print(
            f"| {i} | `{row['job']}` | {row['avg']} | "
            f"{', '.join(row['runtimes']) or 'N/A'} | "
            f"{', '.join(row['build_ids']) or 'N/A'} | "
            f"{', '.join(row['results']) or 'N/A'} |"
        )

def print_csv(rows: list[dict[str, Any]]) -> None:
    writer = csv.DictWriter(
        sys.stdout,
        fieldnames=["rank", "job", "avg", "avg_seconds", "runtimes", "build_ids", "results", "prow_urls"],
    )
    writer.writeheader()
    for i, row in enumerate(rows, 1):
        writer.writerow({
            "rank": i,
            "job": row["job"],
            "avg": row["avg"],
            "avg_seconds": row["avg_seconds"],
            "runtimes": ";".join(row["runtimes"]),
            "build_ids": ";".join(row["build_ids"]),
            "results": ";".join(row["results"]),
            "prow_urls": ";".join(row["prow_urls"]),
        })

def main() -> int:
    parser = argparse.ArgumentParser(
        epilog=(
            "Example:\n"
            "  python prow_runtimes.py --kueue-periodics --limit 5 --only-success\n\n"
            "The command is particularly useful for:\n"
            "  (1) determining the longest taking CI jobs as targets for optimization,\n"
            "  (2) setting up the thresholds for CI performance jobs."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--job", action="append", help="Job name, logs/... prefix, gs:// prefix, or storage URL. Repeatable.")
    group.add_argument("--kueue-periodics", action="store_true", help="Read all periodic-kueue* jobs from kueue-periodics-main.yaml.")
    parser.add_argument("--bucket", default=DEFAULT_BUCKET)
    parser.add_argument("--limit", type=int, default=5)
    parser.add_argument("--max-to-check", type=int, default=200)
    parser.add_argument("--only-success", action="store_true")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--csv", action="store_true")
    parser.add_argument("--verbose", "-v", action="store_true", help="Enable verbose output (prints raw data from URLs).")

    if len(sys.argv) == 1:
        parser.print_help()
        return 0

    args = parser.parse_args()

    jobs = load_kueue_periodic_jobs(verbose=args.verbose) if args.kueue_periodics else args.job
    rows = []

    for job in jobs:
        print("checking: " + job, file=sys.stderr)
        try:
            builds = last_n_builds(
                job,
                limit=args.limit,
                bucket=args.bucket,
                only_success=args.only_success,
                max_to_check=args.max_to_check,
            )
            rows.append(summarize(parse_gs_prefix(job, bucket=args.bucket)[0], builds))
        except Exception as e:
            rows.append({
                "job": parse_gs_prefix(job, bucket=args.bucket)[0],
                "build_count": 0,
                "avg_seconds": None,
                "avg": "ERROR",
                "runtimes": [],
                "build_ids": [],
                "results": [f"{type(e).__name__}: {e}"],
                "prow_urls": [],
            })

    rows.sort(key=lambda r: r["avg_seconds"] if r["avg_seconds"] is not None else -1, reverse=True)

    if args.json:
        print(json.dumps(rows, indent=2, default=str))
    elif args.csv:
        print_csv(rows)
    else:
        print_markdown(rows)

    return 0

if __name__ == "__main__":
    raise SystemExit(main())
