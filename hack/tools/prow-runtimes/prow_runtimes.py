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
KUEUE_PRESUBMITS_URL = (
    "https://raw.githubusercontent.com/kubernetes/test-infra/master/config/jobs/kubernetes-sigs/kueue/kueue-presubmits-main.yaml"
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

def http_get(url: str, *, tries: int = 4, timeout: int = 30, verbose: bool = False) -> bytes:
    """Fetches the content of a URL with retries.

    Example:
        data = http_get("https://storage.googleapis.com/bucket/file.txt")
    """
    if verbose:
        print(f"    [HTTP GET] {url}", file=sys.stderr)
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

def http_json(url: str, *, verbose: bool = False) -> dict[str, Any]:
    """Fetches a URL and parses its content as JSON.

    Example:
        data = http_json("https://storage.googleapis.com/bucket/started.json")
    """
    return json.loads(http_get(url, verbose=verbose).decode("utf-8"))

def join_path(*parts: str) -> str:
    """Joins path segments with a single slash, stripping slashes from individual parts.

    Example:
        join_path("pr-logs", "directory", "job") -> "pr-logs/directory/job"
        join_path("logs/", "/job/") -> "logs/job"
    """
    return "/".join(str(p).strip("/") for p in parts if p)

def get_basename(path: str) -> str:
    """Returns the last component of a path, ignoring trailing slashes.

    Example:
        get_basename("path/to/job") -> "job"
        get_basename("job") -> "job"
    """
    return str(path).strip("/").split("/")[-1]

def parse_gs_url(url: str) -> tuple[str, str]:
    """Parses a GCS URL into (bucket, path). Supports gs:// and https:// formats.

    Example:
        parse_gs_url("gs://kubernetes-ci-logs/logs/job") -> ("kubernetes-ci-logs", "logs/job")
        parse_gs_url("https://storage.googleapis.com/bucket/path") -> ("bucket", "path")
    """
    rest = url.removeprefix("gs://").removeprefix("https://storage.googleapis.com/")
    if rest == url:
        raise ValueError(f"invalid GCS URL: {url}")
    bucket, _, path = rest.partition("/")
    return bucket, path.rstrip("/")

def parse_gs_prefix(job_or_prefix: str, bucket: str = DEFAULT_BUCKET) -> tuple[str, str, str]:
    """Converts a job name or GCS prefix into (display_name, bucket, path).

    If the input is a GCS URL, it extracts the bucket and path.
    If the input is a job name starting with 'pull-kueue', it assumes it is a presubmit
    stored in pr-logs/directory/.
    Otherwise, it assumes it is a periodic job stored in logs/.

    Example:
        parse_gs_prefix("pull-kueue-test") -> ("pull-kueue-test", "kubernetes-ci-logs", "pr-logs/directory/pull-kueue-test")
        parse_gs_prefix("periodic-kueue-test") -> ("periodic-kueue-test", "kubernetes-ci-logs", "logs/periodic-kueue-test")
        parse_gs_prefix("gs://my-bucket/logs/my-job") -> ("my-job", "my-bucket", "logs/my-job")
    """
    value = job_or_prefix.strip().rstrip("/")

    if value.startswith(("gs://", "https://storage.googleapis.com/")):
        bucket_name, path = parse_gs_url(value)
        # For gs://bucket, path is empty, so we use bucket name as display name
        return get_basename(path or bucket_name), bucket_name, path

    if value.startswith(("logs/", "pr-logs/")):
        return get_basename(value), bucket, value

    if value.startswith("pull-kueue"):
        # Kueue presubmits are located in pr-logs/directory/
        return value, bucket, join_path("pr-logs", "directory", value)

    # All other jobs are assumed to be periodic jobs in logs/
    return value, bucket, join_path("logs", value)

def object_url(bucket: str, *path_parts: str) -> str:
    """Builds a public storage URL for a GCS object.

    Example:
        object_url("my-bucket", "logs", "job", "started.json") -> "https://storage.googleapis.com/my-bucket/logs/job/started.json"
    """
    path = join_path(*path_parts)
    return f"https://storage.googleapis.com/{bucket}/{urllib.parse.quote(path, safe='/')}"

def resolve_gcs_base(bucket: str, prefix: str, build_id: str, *, verbose: bool = False) -> tuple[str, str] | None:
    """Resolves the actual GCS location of build artifacts.

    For presubmits (paths starting with pr-logs/directory/), this involves reading
    a .txt pointer file from the directory to find the actual location of the
    artifacts (which may be in a different bucket).

    Example:
        # Standard periodic job
        resolve_gcs_base("bucket", "logs/job", "123") -> ("bucket", "logs/job/123")

        # Presubmit with redirection (assuming 123.txt contains "gs://foo/bar")
        resolve_gcs_base("bucket", "pr-logs/directory/job", "123") -> ("foo", "bar")
    """
    if prefix.startswith("pr-logs/directory/"):
        pointer_path = join_path(prefix, f"{build_id}.txt")
        if verbose:
            print(f"  - Resolving presubmit redirection pointer gs://{bucket}/{pointer_path}...", file=sys.stderr)
        try:
            real_gs_url = http_get(object_url(bucket, pointer_path), verbose=verbose).decode("utf-8").strip()
            return parse_gs_url(real_gs_url)
        except (urllib.error.HTTPError, ValueError) as e:
            if isinstance(e, urllib.error.HTTPError) and e.code == 404:
                return None
            raise
    return bucket, join_path(prefix, build_id)

def list_build_ids(bucket: str, prefix: str, *, verbose: bool = False) -> tuple[list[str], int]:
    """Lists build IDs for a job by querying GCS.

    Returns a tuple of (build_ids, pages_followed).
    """
    if verbose:
        print(f"Retrieving build list from GCS (gs://{bucket}/{prefix})...", file=sys.stderr)
    ids: list[str] = []
    page_token: str | None = None
    pages = 0

    is_presubmit_dir = prefix.startswith("pr-logs/directory/")

    while True:
        pages += 1
        params = {
            "prefix": f"{prefix.strip('/')}/",
            "delimiter": "/",
            "fields": "nextPageToken,prefixes,items(name)" if is_presubmit_dir else "nextPageToken,prefixes",
        }
        if page_token:
            params["pageToken"] = page_token

        url = f"https://storage.googleapis.com/storage/v1/b/{bucket}/o?{urllib.parse.urlencode(params)}"
        if verbose:
            print(f"  Requesting page {pages} from GCS API...", file=sys.stderr)
        data = http_json(url, verbose=verbose)

        page_ids = 0
        if is_presubmit_dir:
            for item in data.get("items", []):
                name = item["name"]
                if name.endswith(".txt"):
                    build_id = get_basename(name.removesuffix(".txt"))
                    if build_id.isdigit():
                        ids.append(build_id)
                        page_ids += 1
        else:
            for common_prefix in data.get("prefixes", []):
                build_id = get_basename(common_prefix)
                if build_id.isdigit():
                    ids.append(build_id)
                    page_ids += 1

        if verbose:
            print(f"    Page {pages} returned {page_ids} builds (total accumulated: {len(ids)})", file=sys.stderr)

        page_token = data.get("nextPageToken")
        if not page_token:
            break

    return sorted(set(ids), key=int, reverse=True), pages

def parse_ts(value: Any) -> dt.datetime:
    """Parses a timestamp value (numeric or ISO string) into a UTC datetime.

    Example:
        parse_ts(1716554737) -> dt.datetime(2024, 5, 24, 12, 45, 37, tzinfo=dt.timezone.utc)
        parse_ts("2024-05-24T12:45:37Z") -> dt.datetime(2024, 5, 24, 12, 45, 37, tzinfo=dt.timezone.utc)
    """
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
    """Returns the value of the first key from the list that is present in the dictionary.

    Example:
        first_present({"a": 1, "b": 2}, ["c", "b"]) -> 2
    """
    for key in keys:
        if key in d and d[key] not in (None, ""):
            return d[key]
    raise KeyError(f"none of these keys found: {list(keys)}")

def read_build_runtime(job: str, bucket: str, prefix: str, build_id: str, *, verbose: bool = False) -> BuildRuntime | None:
    """Reads started.json and finished.json for a build and calculates its runtime.

    Example:
        read_build_runtime("my-job", "bucket", "logs/my-job", "123")
    """
    res = resolve_gcs_base(bucket, prefix, build_id, verbose=verbose)
    if res is None:
        return None
    bucket, base = res

    if verbose:
        print(f"  - Fetching started.json and finished.json from gs://{bucket}/{base}...", file=sys.stderr)
    try:
        started = http_json(object_url(bucket, base, "started.json"), verbose=verbose)
        finished = http_json(object_url(bucket, base, "finished.json"), verbose=verbose)
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
    verbose: bool = False,
) -> list[BuildRuntime]:
    """Fetches the runtime data for the last N builds of a job.

    Example:
        last_n_builds("pull-kueue-test", limit=5)
    """
    job, bucket, prefix = parse_gs_prefix(job_or_prefix, bucket=bucket)
    if verbose:
        print(f"Parsed job: {job}, bucket: {bucket}, prefix: {prefix}", file=sys.stderr)
    out: list[BuildRuntime] = []

    build_ids, pages = list_build_ids(bucket, prefix, verbose=verbose)
    
    print(f"Found {len(build_ids)} build candidates in GCS (followed {pages} pages).", file=sys.stderr)
    target_desc = f"{limit} successful runs" if only_success else f"{limit} latest runs"
    print(f"Checking up to {max_to_check} builds to find {target_desc}...", file=sys.stderr)

    for i, build_id in enumerate(build_ids[:max_to_check], 1):
        print(f"Checking build {build_id} ({i}/{min(len(build_ids), max_to_check)})...", file=sys.stderr)
        build = read_build_runtime(job, bucket, prefix, build_id, verbose=verbose)
        if build is None:
            print(f"  -> Skipped ({len(out)}/{limit}): build data missing", file=sys.stderr)
            continue
        if only_success and build.result not in {"SUCCESS", "success", "true", "True"}:
            print(f"  -> Skipped ({len(out)}/{limit}): result is {build.result}", file=sys.stderr)
            continue
        
        out.append(build)
        print(f"  -> Found! ({len(out)}/{limit}). Result: {build.result}, Duration: {fmt_duration(build.duration_seconds)}", file=sys.stderr)
        if len(out) >= limit:
            print(f"Stopping search (found {limit} builds).", file=sys.stderr)
            break

    return out

def load_kueue_jobs(url: str, job_prefix: str, *, verbose: bool = False) -> list[str]:
    """Fetches job names from a remote Prow configuration YAML file.

    Example:
        load_kueue_jobs(KUEUE_PERIODICS_URL, "periodic-kueue")
    """
    if verbose:
        print(f"Fetching Kueue jobs config from remote YAML...", file=sys.stderr)
    text = http_get(url, verbose=verbose).decode("utf-8")
    if verbose:
        print(text, file=sys.stderr)
    pattern = rf"(?m)^\s*-?\s+name:\s+({job_prefix}[-A-Za-z0-9_.]+)\s*$"
    names = re.findall(pattern, text)
    return sorted(dict.fromkeys(names))

def load_kueue_periodic_jobs(*, verbose: bool = False) -> list[str]:
    """Loads all periodic jobs configured for Kueue."""
    return load_kueue_jobs(KUEUE_PERIODICS_URL, "periodic-kueue", verbose=verbose)

def load_kueue_presubmit_jobs(*, verbose: bool = False) -> list[str]:
    """Loads all presubmit jobs configured for Kueue."""
    return load_kueue_jobs(KUEUE_PRESUBMITS_URL, "pull-kueue", verbose=verbose)

def fmt_duration(seconds: float | int) -> str:
    """Formats a duration in seconds into a human-readable string (MM:SS or HH:MM:SS).

    Example:
        fmt_duration(65) -> "1:05"
        fmt_duration(3665) -> "1:01:05"
    """
    seconds = int(round(seconds))
    h, rem = divmod(seconds, 3600)
    m, s = divmod(rem, 60)
    return f"{h}:{m:02d}:{s:02d}" if h else f"{m}:{s:02d}"

def summarize(job: str, builds: list[BuildRuntime]) -> dict[str, Any]:
    """Calculates statistics and prepares a summary dictionary for a set of builds.

    Example:
        summarize("my-job", [BuildRuntime(...)])
    """
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
    """Prints a summary table in Markdown format."""
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
    """Prints summary data in CSV format."""
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
    group.add_argument("--kueue-presubmits", action="store_true", help="Read all pull-kueue* jobs from kueue-presubmits-main.yaml.")
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

    if args.kueue_periodics:
        jobs = load_kueue_periodic_jobs(verbose=args.verbose)
    elif args.kueue_presubmits:
        jobs = load_kueue_presubmit_jobs(verbose=args.verbose)
    else:
        jobs = args.job

    rows = []

    for job in jobs:
        job_name, _, _ = parse_gs_prefix(job, bucket=args.bucket)
        print(f"Checking job: {job}", file=sys.stderr)
        try:
            builds = last_n_builds(
                job,
                limit=args.limit,
                bucket=args.bucket,
                only_success=args.only_success,
                max_to_check=args.max_to_check,
                verbose=args.verbose,
            )
            rows.append(summarize(job_name, builds))
        except Exception as e:
            rows.append({
                "job": job_name,
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
