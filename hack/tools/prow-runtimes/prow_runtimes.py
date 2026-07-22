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

import argparse
import contextlib
import csv
import datetime as dt
import json
import re
import statistics
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from typing import Any, Dict, Iterable, Iterator, List, Optional, Tuple, Union

DEFAULT_BUCKET = "kubernetes-ci-logs"
KUEUE_PERIODICS_URL = (
    "https://raw.githubusercontent.com/kubernetes/test-infra/master/config/jobs/"
    "kubernetes-sigs/kueue/kueue-periodics-main.yaml"
)
KUEUE_PRESUBMITS_URL = (
    "https://raw.githubusercontent.com/kubernetes/test-infra/master/config/jobs/"
    "kubernetes-sigs/kueue/kueue-presubmits-main.yaml"
)

MERGE_READY = "merge-ready"
REGULAR = "regular"
UNKNOWN = "unknown"
SUCCESS_RESULTS = {"SUCCESS", "success", "true", "True"}
DEFAULT_HTTP_TRIES = 5
DEFAULT_RETRY_BASE_DELAY_SECONDS = 2.0
DEFAULT_WORKERS = 8
DEFAULT_DAYS = 30
DEFAULT_THRESHOLD_SECONDS = 18 * 60
DEFAULT_THRESHOLD_MIN_SAMPLES = 2
CREATED_BY_TIDE_LABEL = "created-by-tide"

_LOG_LOCK = threading.Lock()
_LOG_CONTEXT = threading.local()

@dataclass(frozen=True)
class BuildRuntime:
    job: str
    build_id: str
    result: str
    start: dt.datetime
    finish: dt.datetime
    duration_seconds: int
    prow_url: str
    merge_readiness: str = UNKNOWN

def _current_log_context() -> str:
    """Returns the log context bound to the current worker thread.

    The context is normally the Prow job name. The default is "global"
    for setup and final-report messages that are not associated with one job.
    """
    return getattr(_LOG_CONTEXT, "value", "global")

@contextlib.contextmanager
def log_context(value: str) -> Iterator[None]:
    """Temporarily sets the stderr log context for the current thread.

    This keeps parallel worker output readable:
        with log_context("pull-kueue-test-unit-main"):
            log("Checking build 123...")

    prints:
        [pull-kueue-test-unit-main] Checking build 123...
    """
    old_value = getattr(_LOG_CONTEXT, "value", "global")
    _LOG_CONTEXT.value = value or "global"
    try:
        yield
    finally:
        _LOG_CONTEXT.value = old_value


def log(message: str) -> None:
    """Prints a thread-safe stderr log line prefixed with the current context."""
    context = _current_log_context()
    with _LOG_LOCK:
        for line in str(message).splitlines() or [""]:
            print(f"[{context}] {line}", file=sys.stderr)

def http_get(
    url: str,
    *,
    tries: int = DEFAULT_HTTP_TRIES,
    timeout: int = 30,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
    headers: Optional[Dict[str, str]] = None,
) -> bytes:
    """Fetches the content of a URL with exponential backoff retries.

    Example:
        data = http_get("https://storage.googleapis.com/bucket/file.txt")

    404 responses are treated as permanent misses and are not retried, because
    the caller commonly uses them to detect missing Prow artifacts. Other HTTP
    and URL errors are retried with retry_base_delay * 2**attempt seconds.
    """
    if verbose:
        log(f"[HTTP GET] {url}")

    last_exc = None  # type: Optional[Exception]
    for attempt in range(tries):
        try:
            request_headers = {"User-Agent": "kueue-prow-runtime/1.0"}
            if headers:
                request_headers.update(headers)
            req = urllib.request.Request(url, headers=request_headers)
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.read()
        except urllib.error.HTTPError as e:
            if e.code == 404:
                raise
            last_exc = e
        except urllib.error.URLError as e:
            last_exc = e

        if attempt == tries - 1:
            break

        delay = retry_base_delay * (2 ** attempt)
        retry_after = None
        if isinstance(last_exc, urllib.error.HTTPError):
            retry_after = last_exc.headers.get("Retry-After")
        if retry_after:
            try:
                delay = max(delay, float(retry_after))
            except ValueError:
                pass

        if verbose:
            log(
                f"[HTTP RETRY] {url} failed with {type(last_exc).__name__}: {last_exc}; "
                f"retrying in {delay:.1f}s ({attempt + 2}/{tries})"
            )
        time.sleep(delay)

    raise last_exc  # type: ignore[misc]

def http_json(
    url: str,
    *,
    tries: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
    headers: Optional[Dict[str, str]] = None,
) -> Any:
    """Fetches a URL and parses its content as JSON.

    Example:
        data = http_json("https://storage.googleapis.com/bucket/started.json")
    """
    return json.loads(
        http_get(
            url,
            tries=tries,
            retry_base_delay=retry_base_delay,
            verbose=verbose,
            headers=headers,
        ).decode("utf-8")
    )

def join_path(*parts: str) -> str:
    """Joins path segments with a single slash, stripping slashes from parts.

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

def strip_prefix(value: str, prefix: str) -> str:
    """Returns value without prefix when present.

    This is the Python 3.8-compatible replacement for str.removeprefix().
    """
    if value.startswith(prefix):
        return value[len(prefix) :]
    return value

def strip_suffix(value: str, suffix: str) -> str:
    """Returns value without suffix when present.

    This is the Python 3.8-compatible replacement for str.removesuffix().
    """
    if value.endswith(suffix):
        return value[: -len(suffix)]
    return value

def parse_gs_url(url: str) -> Tuple[str, str]:
    """Parses a GCS URL into (bucket, path).

    Supports gs:// and https://storage.googleapis.com/ formats.

    Example:
        parse_gs_url("gs://kubernetes-ci-logs/logs/job")
            -> ("kubernetes-ci-logs", "logs/job")
        parse_gs_url("https://storage.googleapis.com/bucket/path")
            -> ("bucket", "path")
    """
    rest = strip_prefix(url, "gs://")
    rest = strip_prefix(rest, "https://storage.googleapis.com/")
    if rest == url:
        raise ValueError(f"invalid GCS URL: {url}")
    bucket, _, path = rest.partition("/")
    return bucket, path.rstrip("/")

def parse_gs_prefix(job_or_prefix: str, bucket: str = DEFAULT_BUCKET) -> Tuple[str, str, str]:
    """Converts a job name or GCS prefix into (display_name, bucket, path).

    If the input is a GCS URL, it extracts the bucket and path. If the input
    is a job name starting with 'pull-kueue', it assumes it is a presubmit
    stored in pr-logs/directory/. Otherwise, it assumes the job is stored in
    logs/ as a periodic/postsubmit-style job.

    Example:
        parse_gs_prefix("pull-kueue-test")
            -> ("pull-kueue-test", "kubernetes-ci-logs", "pr-logs/directory/pull-kueue-test")
        parse_gs_prefix("periodic-kueue-test")
            -> ("periodic-kueue-test", "kubernetes-ci-logs", "logs/periodic-kueue-test")
        parse_gs_prefix("gs://my-bucket/logs/my-job")
            -> ("my-job", "my-bucket", "logs/my-job")
    """
    value = job_or_prefix.strip().rstrip("/")

    if value.startswith(("gs://", "https://storage.googleapis.com/")):
        bucket_name, path = parse_gs_url(value)
        return get_basename(path or bucket_name), bucket_name, path

    if value.startswith(("logs/", "pr-logs/")):
        return get_basename(value), bucket, value

    if value.startswith("pull-kueue"):
        return value, bucket, join_path("pr-logs", "directory", value)

    return value, bucket, join_path("logs", value)


def object_url(bucket: str, *path_parts: str) -> str:
    """Builds a public storage URL for a GCS object.

    Example:
        object_url("my-bucket", "logs", "job", "started.json")
            -> "https://storage.googleapis.com/my-bucket/logs/job/started.json"
    """
    path = join_path(*path_parts)
    return f"https://storage.googleapis.com/{bucket}/{urllib.parse.quote(path, safe='/')}"

def resolve_gcs_base(
    bucket: str,
    prefix: str,
    build_id: str,
    *,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> Optional[Tuple[str, str]]:
    """Resolves the actual GCS location of build artifacts.

    For presubmits, paths under pr-logs/directory/ contain small .txt pointer
    files whose content is the real gs:// URL of the build artifacts. Periodic
    jobs normally store artifacts directly under logs/<job>/<build_id>.

    Example:
        resolve_gcs_base("bucket", "logs/job", "123")
            -> ("bucket", "logs/job/123")

        If pr-logs/directory/job/123.txt contains gs://foo/bar:
        resolve_gcs_base("bucket", "pr-logs/directory/job", "123")
            -> ("foo", "bar")
    """
    if prefix.startswith("pr-logs/directory/"):
        pointer_path = join_path(prefix, f"{build_id}.txt")
        if verbose:
            log(f"Resolving presubmit redirection pointer gs://{bucket}/{pointer_path}...")
        try:
            real_gs_url = http_get(
                object_url(bucket, pointer_path),
                tries=retry_attempts,
                retry_base_delay=retry_base_delay,
                verbose=verbose,
            ).decode("utf-8").strip()
            return parse_gs_url(real_gs_url)
        except (urllib.error.HTTPError, ValueError) as e:
            if isinstance(e, urllib.error.HTTPError) and e.code == 404:
                return None
            raise

    return bucket, join_path(prefix, build_id)

def list_build_ids(
    bucket: str,
    prefix: str,
    *,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> Tuple[List[str], int]:
    """Lists build IDs for a job by querying GCS.

    Returns:
        A tuple of (build_ids, pages_followed), where build_ids are sorted
        newest-first. For presubmit directories, build IDs are extracted from
        .txt pointer objects; for direct log directories, they are extracted
        from common prefixes returned by the GCS listing API.
    """
    if verbose:
        log(f"Retrieving build list from GCS (gs://{bucket}/{prefix})...")

    ids = []  # type: List[str]
    page_token = None  # type: Optional[str]
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
            log(f"Requesting page {pages} from GCS API...")

        data = http_json(url, tries=retry_attempts, retry_base_delay=retry_base_delay, verbose=verbose)
        page_ids = 0

        if is_presubmit_dir:
            for item in data.get("items", []):
                name = item["name"]
                if name.endswith(".txt"):
                    build_id = get_basename(strip_suffix(name, ".txt"))
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
            log(f"Page {pages} returned {page_ids} builds (total accumulated: {len(ids)})")

        page_token = data.get("nextPageToken")
        if not page_token:
            break

    return sorted(set(ids), key=int, reverse=True), pages

def parse_ts(value: Any) -> dt.datetime:
    """Parses a timestamp value (numeric or ISO string) into a UTC datetime.

    Example:
        parse_ts(1716554737)
            -> dt.datetime(2024, 5, 24, 12, 45, 37, tzinfo=dt.timezone.utc)
        parse_ts("2024-05-24T12:45:37Z")
            -> dt.datetime(2024, 5, 24, 12, 45, 37, tzinfo=dt.timezone.utc)
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

def first_present(d: Dict[str, Any], keys: Iterable[str]) -> Any:
    """Returns the value of the first requested key present in the dictionary.

    Example:
        first_present({"a": 1, "b": 2}, ["c", "b"]) -> 2
    """
    for key in keys:
        if key in d and d[key] not in (None, ""):
            return d[key]
    raise KeyError(f"none of these keys found: {list(keys)}")

def read_json_object(
    bucket: str,
    base: str,
    filename: str,
    *,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> Optional[Dict[str, Any]]:
    """Reads a JSON object from a build artifact directory if it exists.

    Missing optional metadata files are reported as None rather than errors,
    which lets callers use prowjob.json/podinfo.json opportunistically while
    still supporting older or incomplete build artifact directories.

    Example:
        read_json_object("bucket", "logs/job/123", "prowjob.json")
    """
    try:
        data = http_json(
            object_url(bucket, base, filename),
            tries=retry_attempts,
            retry_base_delay=retry_base_delay,
            verbose=verbose,
        )
        if isinstance(data, dict):
            return data
        return None
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return None
        raise


def has_created_by_tide(obj: Optional[Dict[str, Any]]) -> bool:
    """Returns True when parsed Prow/GCS metadata has created-by-tide=true.

    Tide-created presubmits can still have spec.type=presubmit, so this script
    does not rely on the ProwJob type alone. The label is checked at the common
    metadata.labels and labels locations, followed by a conservative recursive
    search for the exact created-by-tide=true key/value pair in nested metadata.
    """
    if not isinstance(obj, dict):
        return False

    labels_candidates = []  # type: List[Any]
    metadata = obj.get("metadata")
    if isinstance(metadata, dict):
        labels_candidates.append(metadata.get("labels"))
    labels_candidates.append(obj.get("labels"))

    for labels in labels_candidates:
        if isinstance(labels, dict) and str(labels.get(CREATED_BY_TIDE_LABEL, "")).lower() == "true":
            return True

    # Some Prow metadata layouts nest pod labels more deeply. Keep this exact-key
    # recursive fallback, but only accept the explicit created-by-tide=true signal.
    for value in obj.values():
        if isinstance(value, dict):
            if has_created_by_tide(value):
                return True
        elif isinstance(value, list):
            for item in value:
                if isinstance(item, dict) and has_created_by_tide(item):
                    return True

    return False


def classify_merge_readiness(
    prowjob: Optional[Dict[str, Any]],
    podinfo: Optional[Dict[str, Any]],
) -> str:
    """Classifies build readiness using only Prow/GCS metadata.

    The intentionally narrow criterion is created-by-tide=true. This avoids
    GitHub API rate limits and avoids heuristics based on PR status history.

    Returns:
        "merge-ready" when Tide-created metadata is found, otherwise "regular".
    """
    if has_created_by_tide(prowjob) or has_created_by_tide(podinfo):
        return MERGE_READY
    return REGULAR


def read_build_runtime(
    job: str,
    bucket: str,
    prefix: str,
    build_id: str,
    *,
    check_merge_pool: bool = False,
    min_finish_time: Optional[dt.datetime] = None,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> Optional[BuildRuntime]:
    """Reads build artifacts and calculates runtime for one build.

    Runtime is derived from started.json and finished.json. When
    check_merge_pool is enabled, the function also reads optional Prow metadata
    files such as prowjob.json and podinfo.json to classify the build as
    merge-ready or regular. If min_finish_time is provided, builds finished
    before that timestamp are ignored.

    Example:
        read_build_runtime("my-job", "bucket", "logs/my-job", "123")
    """
    res = resolve_gcs_base(
        bucket,
        prefix,
        build_id,
        retry_attempts=retry_attempts,
        retry_base_delay=retry_base_delay,
        verbose=verbose,
    )
    if res is None:
        return None
    artifact_bucket, base = res

    if verbose:
        log(f"Fetching started.json, finished.json and metadata from gs://{artifact_bucket}/{base}...")

    try:
        started = http_json(
            object_url(artifact_bucket, base, "started.json"),
            tries=retry_attempts,
            retry_base_delay=retry_base_delay,
            verbose=verbose,
        )
        finished = http_json(
            object_url(artifact_bucket, base, "finished.json"),
            tries=retry_attempts,
            retry_base_delay=retry_base_delay,
            verbose=verbose,
        )
        prowjob = None
        podinfo = None
        if check_merge_pool:
            prowjob = read_json_object(
                artifact_bucket,
                base,
                "prowjob.json",
                retry_attempts=retry_attempts,
                retry_base_delay=retry_base_delay,
                verbose=verbose,
            )
            podinfo = read_json_object(
                artifact_bucket,
                base,
                "podinfo.json",
                retry_attempts=retry_attempts,
                retry_base_delay=retry_base_delay,
                verbose=verbose,
            )
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return None
        raise

    start = parse_ts(first_present(started, ["timestamp", "startTime", "started"]))
    finish = parse_ts(first_present(finished, ["timestamp", "completionTime", "finished"]))
    if min_finish_time is not None and finish < min_finish_time:
        return None

    seconds = int(round((finish - start).total_seconds()))
    if seconds < 0:
        raise ValueError(f"negative duration for {job} build {build_id}: {seconds}s")

    result = str(finished.get("result") or finished.get("passed") or "unknown")
    merge_readiness = classify_merge_readiness(prowjob, podinfo) if check_merge_pool else UNKNOWN

    return BuildRuntime(
        job=job,
        build_id=build_id,
        result=result,
        start=start,
        finish=finish,
        duration_seconds=seconds,
        prow_url=f"https://prow.k8s.io/view/gs/{artifact_bucket}/{base}",
        merge_readiness=merge_readiness,
    )

def last_n_builds(
    job_or_prefix: str,
    *,
    limit: int,
    bucket: str = DEFAULT_BUCKET,
    only_success: bool = False,
    max_to_check: int = 200,
    only_merge_pool: bool = False,
    check_merge_pool: bool = False,
    days: int = DEFAULT_DAYS,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> List[BuildRuntime]:
    """Fetches runtime data for the last N builds matching the requested filters.

    Example:
        last_n_builds("pull-kueue-test", limit=5)

    The function walks newest-first through up to max_to_check builds. The
    returned list can be filtered to successful builds and/or Tide-created
    merge-pool builds, depending on the command-line flags. With days > 0,
    builds outside the selected time window are ignored.
    """
    job, bucket, prefix = parse_gs_prefix(job_or_prefix, bucket=bucket)
    if verbose:
        log(f"Parsed job: {job}, bucket: {bucket}, prefix: {prefix}")

    out = []  # type: List[BuildRuntime]
    build_ids, pages = list_build_ids(
        bucket,
        prefix,
        retry_attempts=retry_attempts,
        retry_base_delay=retry_base_delay,
        verbose=verbose,
    )
    log(f"Found {len(build_ids)} build candidates in GCS (followed {pages} pages).")

    min_finish_time = None  # type: Optional[dt.datetime]
    if days > 0:
        min_finish_time = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=days)

    filters = []  # type: List[str]
    if only_success:
        filters.append("successful")
    if only_merge_pool:
        filters.append("merge-ready")
    if days > 0:
        filters.append(f"last-{days}d")
    filter_desc = " ".join(filters) if filters else "latest"
    target_desc = f"{limit} {filter_desc} runs"
    log(f"Checking up to {max_to_check} builds to find {target_desc}...")

    for i, build_id in enumerate(build_ids[:max_to_check], 1):
        log(f"Checking build {build_id} ({i}/{min(len(build_ids), max_to_check)})...")

        build = read_build_runtime(
            job,
            bucket,
            prefix,
            build_id,
            check_merge_pool=check_merge_pool or only_merge_pool,
            min_finish_time=min_finish_time,
            retry_attempts=retry_attempts,
            retry_base_delay=retry_base_delay,
            verbose=verbose,
        )
        if build is None:
            log(f" -> Skipped ({len(out)}/{limit}): build data missing or outside selected time window")
            continue

        if only_success and build.result not in SUCCESS_RESULTS:
            log(f" -> Skipped ({len(out)}/{limit}): result is {build.result}")
            continue

        if only_merge_pool and build.merge_readiness != MERGE_READY:
            log(f" -> Skipped ({len(out)}/{limit}): {build.merge_readiness}")
            continue

        out.append(build)
        log(
            f" -> Found! ({len(out)}/{limit}).\n"
            f"    Result: {build.result}, Duration: {fmt_duration(build.duration_seconds)}, "
            f"Merge readiness: {build.merge_readiness}"
        )
        if len(out) >= limit:
            log(f"Stopping search (found {limit} builds).")
            break

    return out

def load_kueue_jobs(
    url: str,
    job_prefix: str,
    *,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> List[str]:
    """Fetches job names from a remote Prow configuration YAML file.

    Example:
        load_kueue_jobs(KUEUE_PERIODICS_URL, "periodic-kueue")

    The parser intentionally uses a small regex rather than a YAML dependency so
    the script stays self-contained and uses only the Python standard library.
    """
    if verbose:
        log("Fetching Kueue jobs config from remote YAML...")
    text = http_get(
        url,
        tries=retry_attempts,
        retry_base_delay=retry_base_delay,
        verbose=verbose,
    ).decode("utf-8")
    if verbose:
        log(text)

    pattern = rf"(?m)^\s*-?\s+name:\s+({job_prefix}[-A-Za-z0-9_.]+)\s*$"
    names = re.findall(pattern, text)
    return sorted(dict.fromkeys(names))

def load_kueue_periodic_jobs(
    *,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> List[str]:
    """Loads all periodic jobs configured for Kueue."""
    return load_kueue_jobs(
        KUEUE_PERIODICS_URL,
        "periodic-kueue",
        retry_attempts=retry_attempts,
        retry_base_delay=retry_base_delay,
        verbose=verbose,
    )

def load_kueue_presubmit_jobs(
    *,
    retry_attempts: int = DEFAULT_HTTP_TRIES,
    retry_base_delay: float = DEFAULT_RETRY_BASE_DELAY_SECONDS,
    verbose: bool = False,
) -> List[str]:
    """Loads all presubmit jobs configured for Kueue."""
    return load_kueue_jobs(
        KUEUE_PRESUBMITS_URL,
        "pull-kueue",
        retry_attempts=retry_attempts,
        retry_base_delay=retry_base_delay,
        verbose=verbose,
    )

def fmt_duration(seconds: Union[float, int]) -> str:
    """Formats a duration in seconds as MM:SS or HH:MM:SS.

    Example:
        fmt_duration(65) -> "1:05"
        fmt_duration(3665) -> "1:01:05"
    """
    seconds = int(round(seconds))
    h, rem = divmod(seconds, 3600)
    m, s = divmod(rem, 60)
    return f"{h}:{m:02d}:{s:02d}" if h else f"{m}:{s:02d}"

def summarize(job: str, builds: List[BuildRuntime]) -> Dict[str, Any]:
    """Calculates statistics and prepares a summary dictionary for builds.

    Example:
        summarize("my-job", [BuildRuntime(...)])

    The summary format is shared by Markdown, CSV and JSON output modes.
    """
    durations = [b.duration_seconds for b in builds]
    sorted_durations = sorted(durations)
    second_longest_seconds = sorted_durations[-2] if len(sorted_durations) >= 2 else None
    return {
        "job": job,
        "build_count": len(builds),
        "avg_seconds": statistics.mean(durations) if durations else None,
        "avg": fmt_duration(statistics.mean(durations)) if durations else "N/A",
        "max_seconds": max(durations) if durations else None,
        "max": fmt_duration(max(durations)) if durations else "N/A",
        "second_longest_seconds": second_longest_seconds,
        "second_longest": fmt_duration(second_longest_seconds) if second_longest_seconds is not None else "N/A",
        "runtimes": [fmt_duration(x) for x in durations],
        "build_ids": [b.build_id for b in builds],
        "results": [b.result for b in builds],
        "merge_readiness": [b.merge_readiness for b in builds],
        "prow_urls": [b.prow_url for b in builds],
    }

def parse_duration_seconds(value: str) -> int:
    """Parses a human-friendly duration into seconds.

    Accepted examples:
        18m     -> 1080
        1080s   -> 1080
        18:00   -> 1080
        1h30m   -> 5400
    """
    text = str(value).strip().lower()
    if not text:
        raise argparse.ArgumentTypeError("duration must not be empty")

    if re.fullmatch(r"\d+", text):
        return int(text)

    if ":" in text:
        parts = text.split(":")
        if len(parts) == 2 and all(p.isdigit() for p in parts):
            minutes, seconds = [int(p) for p in parts]
            return minutes * 60 + seconds
        if len(parts) == 3 and all(p.isdigit() for p in parts):
            hours, minutes, seconds = [int(p) for p in parts]
            return hours * 3600 + minutes * 60 + seconds
        raise argparse.ArgumentTypeError(f"invalid duration: {value}")

    total = 0
    position = 0
    matched = False
    for match in re.finditer(r"(\d+)(h|m|s)", text):
        if match.start() != position:
            raise argparse.ArgumentTypeError(f"invalid duration: {value}")
        amount = int(match.group(1))
        unit = match.group(2)
        if unit == "h":
            total += amount * 3600
        elif unit == "m":
            total += amount * 60
        else:
            total += amount
        position = match.end()
        matched = True

    if not matched or position != len(text):
        raise argparse.ArgumentTypeError(f"invalid duration: {value}")
    return total

def threshold_label(seconds: int) -> str:
    """Formats a threshold for diagnostics using both seconds and MM:SS."""
    return f"{fmt_duration(seconds)} ({seconds}s)"

def threshold_value(row: Dict[str, Any], stat: str) -> Optional[float]:
    """Returns the selected runtime statistic from a summary row.

    The threshold mode supports average runtime, maximum runtime, and the
    second-longest runtime. The second-longest runtime is useful for periodic
    validation because it tolerates one isolated slow outlier while still
    catching repeated regressions.
    """
    if stat == "avg":
        return row.get("avg_seconds")
    if stat == "max":
        return row.get("max_seconds")
    if stat == "second_longest":
        return row.get("second_longest_seconds")
    raise ValueError(f"unsupported threshold stat: {stat}")

def threshold_violations(
    rows: List[Dict[str, Any]],
    threshold_seconds: int,
    stat: str,
    min_samples: int = DEFAULT_THRESHOLD_MIN_SAMPLES,
) -> Tuple[List[Dict[str, Any]], List[Dict[str, Any]]]:
    """Returns threshold violations and rows skipped due to insufficient data.

    Rows with fewer than min_samples matching builds are skipped from threshold
    enforcement because their statistics are not reliable enough for a periodic
    regression guard. They remain visible in the report table.
    """
    violations = []  # type: List[Dict[str, Any]]
    skipped = []  # type: List[Dict[str, Any]]
    for row in rows:
        if row.get("build_count", 0) < min_samples:
            skipped.append(row)
            continue
        value = threshold_value(row, stat)
        if value is not None and value > threshold_seconds:
            violations.append(row)
    return violations, skipped

def print_markdown(rows: List[Dict[str, Any]]) -> None:
    """Prints a summary table in Markdown format.

    The Merge readiness column is useful when --only-merge-pool is enabled, but
    it is always emitted so regular and merge-ready runs are visible in normal
    presubmit reports too.
    """
    print("| Rank | Job | Builds | Avg | Max | Last runtimes | Build IDs | Results | Merge readiness |")
    print("|---:|---|---:|---:|---:|---|---|---|---|")
    for i, row in enumerate(rows, 1):
        print(
            f"| {i} | `{row['job']}` | {row['build_count']} | {row['avg']} | {row['max']} | "
            f"{', '.join(row['runtimes']) or 'N/A'} | "
            f"{', '.join(row['build_ids']) or 'N/A'} | "
            f"{', '.join(row['results']) or 'N/A'} | "
            f"{', '.join(row['merge_readiness']) or 'N/A'} |"
        )

def print_csv(rows: List[Dict[str, Any]]) -> None:
    """Prints summary data in CSV format.

    List-valued fields are semicolon-separated to keep each job on one CSV row.
    """
    writer = csv.DictWriter(
        sys.stdout,
        fieldnames=[
            "rank",
            "job",
            "build_count",
            "avg",
            "avg_seconds",
            "max",
            "max_seconds",
            "second_longest",
            "second_longest_seconds",
            "runtimes",
            "build_ids",
            "results",
            "merge_readiness",
            "prow_urls",
        ],
    )
    writer.writeheader()
    for i, row in enumerate(rows, 1):
        writer.writerow(
            {
                "rank": i,
                "job": row["job"],
                "build_count": row["build_count"],
                "avg": row["avg"],
                "avg_seconds": row["avg_seconds"],
                "max": row["max"],
                "max_seconds": row["max_seconds"],
                "second_longest": row["second_longest"],
                "second_longest_seconds": row["second_longest_seconds"],
                "runtimes": ";".join(row["runtimes"]),
                "build_ids": ";".join(row["build_ids"]),
                "results": ";".join(row["results"]),
                "merge_readiness": ";".join(row["merge_readiness"]),
                "prow_urls": ";".join(row["prow_urls"]),
            }
        )

def elapsed_duration(start_time: float, end_time: Optional[float] = None) -> str:
    """Formats elapsed wall-clock time from a monotonic start.

    Example:
        start = time.monotonic()
        ...
        log(f"Script finished in {elapsed_duration(start)}")
    """
    if end_time is None:
        end_time = time.monotonic()
    return fmt_duration(end_time - start_time)

def main() -> int:
    start_time = time.monotonic()
    parser = argparse.ArgumentParser(
        epilog=(
            "Example:\n"
            "  python prow_runtimes.py --kueue-periodics --limit 5 --only-success\n"
            "  python prow_runtimes.py --kueue-presubmits --limit 5 --only-success --only-merge-pool\n\n"
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
    parser.add_argument(
        "--days",
        type=int,
        default=DEFAULT_DAYS,
        help="Only include builds finished in the last N days. Defaults to 30. Use 0 to disable.",
    )
    parser.add_argument(
        "--only-merge-pool",
        action="store_true",
        help=(
            "For presubmit jobs, only include builds created by Tide, identified by "
            "created-by-tide=true in Prow/GCS metadata such as prowjob.json or podinfo.json."
        ),
    )
    parser.add_argument(
        "--threshold",
        nargs="?",
        const="18m",
        default=None,
        type=parse_duration_seconds,
        help=(
            "Enable runtime threshold enforcement and return a non-zero exit code when "
            "any sufficiently-sampled reported job exceeds the threshold. Accepts values "
            "like 18m, 1080s, or 18:00. If provided without a value, defaults to 18m."
        ),
    )
    parser.add_argument(
        "--threshold-stat",
        choices=["avg", "max", "second_longest"],
        default="avg",
        help=(
            "Runtime statistic checked by --threshold. Defaults to avg. Use second_longest "
            "to ignore one slow outlier per job."
        ),
    )
    parser.add_argument(
        "--threshold-min-samples",
        type=int,
        default=DEFAULT_THRESHOLD_MIN_SAMPLES,
        help="Skip threshold validation for jobs with fewer than this many matching builds. Defaults to 2.",
    )
    parser.add_argument(
        "--retry-base-delay",
        type=float,
        default=DEFAULT_RETRY_BASE_DELAY_SECONDS,
        help="Base delay in seconds for exponential backoff when HTTP requests fail. Defaults to 2.0.",
    )
    parser.add_argument(
        "--retry-attempts",
        type=int,
        default=DEFAULT_HTTP_TRIES,
        help="Maximum HTTP attempts before failing. Defaults to 5.",
    )
    parser.add_argument(
        "--workers",
        type=int,
        default=DEFAULT_WORKERS,
        help="Number of jobs to process in parallel. Defaults to 8.",
    )
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--csv", action="store_true")
    parser.add_argument("--verbose", "-v", action="store_true", help="Enable verbose output.")

    if len(sys.argv) == 1:
        parser.print_help()
        return 0

    args = parser.parse_args()

    if args.only_merge_pool and args.kueue_periodics:
        parser.error("--only-merge-pool is only valid for presubmit jobs")
    if args.days < 0:
        parser.error("--days must be non-negative")
    if args.threshold is not None and args.threshold < 0:
        parser.error("--threshold must be non-negative")
    if args.threshold_min_samples < 1:
        parser.error("--threshold-min-samples must be at least 1")
    if args.workers < 1:
        parser.error("--workers must be at least 1")
    if args.retry_attempts < 1:
        parser.error("--retry-attempts must be at least 1")
    if args.retry_base_delay < 0:
        parser.error("--retry-base-delay must be non-negative")

    # Kueue presubmit reports should show whether each selected build was
    # created by Tide, even when --only-merge-pool is not filtering the rows.
    should_check_merge_pool = bool(args.only_merge_pool or args.kueue_presubmits)

    if args.kueue_periodics:
        jobs = load_kueue_periodic_jobs(
            retry_attempts=args.retry_attempts,
            retry_base_delay=args.retry_base_delay,
            verbose=args.verbose,
        )
    elif args.kueue_presubmits:
        jobs = load_kueue_presubmit_jobs(
            retry_attempts=args.retry_attempts,
            retry_base_delay=args.retry_base_delay,
            verbose=args.verbose,
        )
    else:
        jobs = args.job

    def process_job(job: str) -> Dict[str, Any]:
        job_name, _, _ = parse_gs_prefix(job, bucket=args.bucket)
        with log_context(job_name):
            log(f"Checking job: {job}")
            try:
                builds = last_n_builds(
                    job,
                    limit=args.limit,
                    bucket=args.bucket,
                    only_success=args.only_success,
                    max_to_check=args.max_to_check,
                    only_merge_pool=args.only_merge_pool,
                    check_merge_pool=should_check_merge_pool,
                    days=args.days,
                    retry_attempts=args.retry_attempts,
                    retry_base_delay=args.retry_base_delay,
                    verbose=args.verbose,
                )
                return summarize(job_name, builds)
            except Exception as e:
                log(f"ERROR: {type(e).__name__}: {e}")
                return {
                    "job": job_name,
                    "build_count": 0,
                    "avg_seconds": None,
                    "avg": "ERROR",
                    "max_seconds": None,
                    "max": "ERROR",
                    "second_longest_seconds": None,
                    "second_longest": "ERROR",
                    "runtimes": [],
                    "build_ids": [],
                    "results": [f"{type(e).__name__}: {e}"],
                    "merge_readiness": [],
                    "prow_urls": [],
                }

    rows = []  # type: List[Dict[str, Any]]
    worker_count = min(args.workers, len(jobs)) if jobs else 1
    with ThreadPoolExecutor(max_workers=worker_count) as executor:
        future_to_job = {executor.submit(process_job, job): job for job in jobs}
        for future in as_completed(future_to_job):
            rows.append(future.result())

    rows.sort(key=lambda r: r["avg_seconds"] if r["avg_seconds"] is not None else -1, reverse=True)

    if args.json:
        print(json.dumps(rows, indent=2, default=str))
    elif args.csv:
        print_csv(rows)
    else:
        print_markdown(rows)

    exit_code = 0
    if args.threshold is not None:
        violations, skipped = threshold_violations(
            rows,
            args.threshold,
            args.threshold_stat,
            min_samples=args.threshold_min_samples,
        )
        if skipped:
            log(
                f"Runtime threshold skipped for {len(skipped)} job(s) with fewer than "
                f"{args.threshold_min_samples} matching builds."
            )
            for row in skipped:
                log(f" - {row['job']}: builds={row.get('build_count', 0)}")

        if violations:
            exit_code = 1
            log(
                f"Runtime threshold exceeded: {len(violations)} job(s) have "
                f"{args.threshold_stat} runtime above {threshold_label(args.threshold)}."
            )
            for row in violations:
                value = threshold_value(row, args.threshold_stat)
                assert value is not None
                log(
                    f" - {row['job']}: {args.threshold_stat}={fmt_duration(value)} "
                    f"builds={', '.join(row['build_ids']) or 'N/A'} "
                    f"runtimes={', '.join(row['runtimes']) or 'N/A'}"
                )
        else:
            log(
                f"Runtime threshold check passed: all sufficiently-sampled jobs have "
                f"{args.threshold_stat} runtime <= {threshold_label(args.threshold)}."
            )

    log(f"Script finished in {elapsed_duration(start_time)}")
    return exit_code

if __name__ == "__main__":
    raise SystemExit(main())
