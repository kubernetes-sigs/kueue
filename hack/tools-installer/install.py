#!/usr/bin/env python3
# Copyright 2026 The Kubernetes Authors.
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

"""Install a pinned upstream release binary, reusing it until its version changes."""

import hashlib
from http.client import HTTPException
import io
import json
import logging
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

LOG = logging.getLogger(__name__)
DOWNLOAD_ATTEMPTS = 7
DOWNLOAD_INITIAL_DELAY = 2
RATE_LIMIT_DELAY = 20

# Keys are executable names; Make supplies the destination path and pinned version.
# Packages default to their module and tags to {version}.
TOOLS = json.loads(Path(__file__).with_name("data.json").read_text())


def log_directory():
    return Path(os.environ.get("INSTALL_LOG_DIR", Path(__file__).resolve().parents[2] / ".cache/tool-installation"))


def configure_logging(name):
    """Keep concise console output and replace each tool's detailed log on invocation."""
    log_dir = log_directory()
    log_dir.mkdir(parents=True, exist_ok=True)
    console = logging.StreamHandler()
    console.setLevel(logging.INFO)
    logfile = logging.FileHandler(log_dir / f"install-{name}.log", mode="w")
    logfile.setLevel(logging.DEBUG)
    logging.basicConfig(
        level=logging.DEBUG,
        format="%(asctime)s %(levelname)s %(message)s",
        handlers=[console, logfile],
    )


def collect_logs():
    """Archive Prow diagnostics without coupling local logs to release artifacts."""
    if not os.environ.get("PROW_JOB_ID") or not os.environ.get("ARTIFACTS"):
        return
    try:
        for handler in logging.getLogger().handlers:
            handler.flush()
        destination = Path(os.environ["ARTIFACTS"]) / "tool-installation"
        if log_directory().resolve() == destination.resolve():
            return
        destination.mkdir(parents=True, exist_ok=True)
        for source in log_directory().glob("install-*.log"):
            shutil.copyfile(source, destination / source.name)
    except Exception as error:
        LOG.warning("Could not collect tool installation logs: %s", error)


class NoReleaseBinary(ValueError):
    """The requested release or a verified binary for this host is unavailable."""


def install_source(name, version, destination):
    tool = TOOLS[name]
    module = tool["module"]
    package = tool.get("package", module)
    LOG.info("Building %s from %s@%s into %s", name, package, version, destination)
    result = subprocess.run(
        ["bash", str(Path(__file__).with_name("source.sh")), str(destination), module, version, package],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    if result.stdout:
        LOG.debug("Source build output for %s:\n%s", name, result.stdout.rstrip())
    result.check_returncode()


def download(url):
    """Match NETWORK_INSTALL_RETRY's seven attempts and exponential backoff."""
    headers = {"User-Agent": "kueue-tool-installer"}
    last_attempt = DOWNLOAD_ATTEMPTS - 1
    for attempt in range(DOWNLOAD_ATTEMPTS):
        LOG.debug("Downloading %s (attempt %d/%d)", url, attempt + 1, DOWNLOAD_ATTEMPTS)
        try:
            with urlopen(Request(url, headers=headers), timeout=120) as response:
                content = response.read()
                LOG.debug("Downloaded %d bytes from %s", len(content), url)
                return content
        except HTTPError as error:
            LOG.warning("Download failed for %s (attempt %d/%d): %s", url, attempt + 1, DOWNLOAD_ATTEMPTS, error)
            if attempt == last_attempt or (error.code not in (408, 429) and error.code < 500):
                raise
            # Respect the fixed rate-limit wait independently of transient backoff.
            delay = RATE_LIMIT_DELAY if error.code == 429 else DOWNLOAD_INITIAL_DELAY * 2 ** attempt
        except (URLError, TimeoutError, HTTPException, ConnectionError) as error:
            LOG.warning("Download failed for %s (attempt %d/%d): %s", url, attempt + 1, DOWNLOAD_ATTEMPTS, error)
            if attempt == last_attempt:
                raise
            delay = DOWNLOAD_INITIAL_DELAY * 2 ** attempt
        LOG.info("Retrying %s in %ds", url, delay)
        time.sleep(delay)


def release(name, version, goos, goarch):
    """Resolve the repository, release tag, and binary asset for a pinned tool."""
    if name not in TOOLS:
        raise ValueError(f"No upstream binary configured for {name}")
    tool = TOOLS[name]
    values = {
        "version": version,
        "number": version.removeprefix("v"),
        "goos": goos,
        "goarch": goarch,
        "platform": {"darwin": "Darwin", "linux": "Linux"}.get(goos, goos),
        "arch": {"amd64": "x86_64", "arm64": "arm64"}.get(goarch, goarch),
    }
    return tool["repository"], tool.get("tag", "{version}").format(**values), tool["asset"].format(**values)


def parse_checksums(text, filename=None):
    """Read SHA256 manifests, including a bare digest for a known binary asset."""
    checksums = {}
    if filename and re.fullmatch(r"[a-fA-F0-9]{64}", text.strip()):
        return {filename: text.strip()}
    for line in text.splitlines():
        match = re.fullmatch(r"([a-fA-F0-9]{64})\s+\*?(.+)", line.strip())
        if match:
            checksums[match[2]] = match[1]
        match = re.fullmatch(r"SHA(?:2-|-)?256 \((.+)\) = ([a-fA-F0-9]{64})", line.strip())
        if match:
            checksums[match[1]] = match[2]
    return checksums


def release_binary(name, version, goos, goarch):
    repo, tag, filename = release(name, version, goos, goarch)
    LOG.debug("Selected release %s@%s, asset %s", repo, tag, filename)
    base = f"https://github.com/{repo}/releases/download/{quote(tag, safe='')}/"
    checksum_file = TOOLS[name]["checksum"].format(
        version=version, number=version.removeprefix("v"), goos=goos, goarch=goarch, asset=filename,
    )
    try:
        checksums = parse_checksums(download(base + checksum_file).decode(), filename)
        if not checksums:
            raise ValueError(f"No SHA256 checksums in {checksum_file}")
        if filename not in checksums:
            raise NoReleaseBinary(f"Upstream checksum manifest has no {filename}")
        content = download(base + filename)
    except HTTPError as error:
        if error.code != 404:
            raise
        # A Go module tag can be available before its release binaries are published.
        raise NoReleaseBinary(f"Upstream release {repo}@{tag} has no verified {filename}") from error
    expected = checksums[filename]
    if hashlib.sha256(content).hexdigest() != expected.lower():
        raise ValueError(f"SHA256 mismatch for {filename}")
    LOG.debug("Verified SHA256 %s for %s", expected.lower(), filename)
    if filename.endswith(".tar.gz"):
        with tarfile.open(fileobj=io.BytesIO(content), mode="r:gz") as archive:
            candidates = [member for member in archive.getmembers() if member.isfile() and Path(member.name).name == name]
            if len(candidates) != 1:
                raise ValueError(f"Expected one {name} binary in {filename}")
            return archive.extractfile(candidates[0]).read()
    return content


def install(name, version, destination):
    goos, goarch = subprocess.check_output(
        [os.environ.get("GO_CMD", "go"), "env", "GOHOSTOS", "GOHOSTARCH"], text=True
    ).split()
    destination = Path(destination)
    marker = destination.with_name(destination.name + ".release-version")
    identity = f"{version} {goos}/{goarch}\n"
    LOG.debug("Installing %s %s for %s/%s into %s", name, version, goos, goarch, destination)
    if destination.is_file() and os.access(destination, os.X_OK) and marker.is_file():
        if marker.read_text() == identity:
            LOG.info("Reusing %s %s for %s/%s", name, version, goos, goarch)
            return
        if marker.read_text() == identity + "source\n":
            LOG.debug("Checking cached source build for %s", name)
            install_source(name, version, destination)
            return
    LOG.info("Downloading %s %s for %s/%s", name, version, goos, goarch)
    try:
        binary = release_binary(name, version, goos, goarch)
    except NoReleaseBinary as error:
        LOG.info("%s; using a source build", error)
        # Preserve unpublished releases and unsupported hosts without hiding integrity failures.
        install_source(name, version, destination)
        marker.write_text(identity + "source\n")
        return
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Only replace a working binary after the download and checksum succeed.
    with tempfile.TemporaryDirectory(dir=destination.parent) as temp:
        staged = Path(temp) / name
        staged.write_bytes(binary)
        staged.chmod(0o755)
        shutil.move(staged, destination)
    marker.write_text(identity)
    LOG.info("Installed %s %s into %s", name, version, destination)


if __name__ == "__main__":
    if sys.argv[1:] == ["--collect-logs"]:
        collect_logs()
    else:
        configure_logging(sys.argv[1])
        try:
            install(*sys.argv[1:])
        except Exception:
            LOG.exception("Tool installation failed")
            sys.exit(1)
        finally:
            collect_logs()
