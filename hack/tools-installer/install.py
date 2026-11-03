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
# Packages default to their module, tags to {version}; no checksum means release API metadata.
TOOLS = json.loads(Path(__file__).with_name("data.json").read_text())


def configure_logging(name):
    """Keep diagnostics in both the build log and a per-tool CI artifact."""
    artifacts = Path(os.environ.get("ARTIFACTS", Path(__file__).resolve().parents[2] / "artifacts"))
    artifacts.mkdir(parents=True, exist_ok=True)
    logging.basicConfig(
        level=logging.DEBUG,
        format="%(asctime)s %(levelname)s %(message)s",
        handlers=[logging.StreamHandler(), logging.FileHandler(artifacts / f"install-{name}.log")],
    )


class NoReleaseBinary(ValueError):
    """The requested release exists but has no binary for this host."""


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
    if url.startswith("https://api.github.com/") and os.environ.get("GH_TOKEN"):
        headers["Authorization"] = f"Bearer {os.environ['GH_TOKEN']}"
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
    """Use the same versions as hack/tools/go.mod, without compiling tools."""
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


def parse_checksums(text):
    """Read standard SHA256 and BSD checksum manifests."""
    checksums = {}
    for line in text.splitlines():
        match = re.fullmatch(r"([a-fA-F0-9]{64})\s+\*?(.+)", line.strip())
        if match:
            checksums[match[2]] = match[1]
        match = re.fullmatch(r"SHA(?:2-|-)?256 \((.+)\) = ([a-fA-F0-9]{64})", line.strip())
        if match:
            checksums[match[1]] = match[2]
    return checksums


def release_assets(name, version, repo, tag):
    checksum_template = TOOLS[name].get("checksum")
    if not checksum_template:
        return json.loads(download(f"https://api.github.com/repos/{repo}/releases/tags/{quote(tag, safe='')}"))["assets"]
    # Prow jobs need no API token for tools with independent checksum manifests.
    base = f"https://github.com/{repo}/releases/download/{quote(tag, safe='')}/"
    checksum_file = checksum_template.format(version=version, number=version.removeprefix("v"))
    checksums = parse_checksums(download(base + checksum_file).decode())
    if not checksums:
        raise ValueError(f"No SHA256 checksums in {checksum_file}")
    return [{"name": asset, "digest": "sha256:" + digest, "browser_download_url": base + asset} for asset, digest in checksums.items()]


def release_binary(name, version, goos, goarch):
    repo, tag, filename = release(name, version, goos, goarch)
    LOG.debug("Selected release %s@%s, asset %s", repo, tag, filename)
    assets = {asset["name"]: asset for asset in release_assets(name, version, repo, tag)}
    if filename not in assets:
        # A partial manifest must not turn a published binary into a source build.
        # API-backed tools already provided the complete published asset list.
        if TOOLS[name].get("checksum"):
            published = json.loads(download(f"https://api.github.com/repos/{repo}/releases/tags/{quote(tag, safe='')}"))["assets"]
            if any(asset["name"] == filename for asset in published):
                raise ValueError(f"No SHA256 checksum for {filename}")
        raise NoReleaseBinary(f"Upstream release {repo}@{tag} has no {filename}")
    asset = assets[filename]
    content = download(asset["browser_download_url"])
    digest = asset.get("digest") or ""
    if digest.startswith("sha256:"):
        expected = digest.removeprefix("sha256:")
    else:
        expected = None
        # Older releases have checksum files but no GitHub asset digest.
        for checksum in assets:
            if "checksums" not in checksum or checksum.endswith((".sig", ".pem", ".bundle")):
                continue
            expected = parse_checksums(download(assets[checksum]["browser_download_url"]).decode()).get(filename)
            if expected:
                break
        if not expected:
            raise ValueError(f"No SHA256 checksum for {filename}")
    if hashlib.sha256(content).hexdigest() != expected.lower():
        raise ValueError(f"SHA256 mismatch for {filename}")
    LOG.debug("Verified SHA256 %s for %s", expected.lower(), filename)
    if filename.endswith(".tar.gz"):
        import io

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
        # Preserve hosts not covered by upstream assets; never hide download or checksum errors.
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
    configure_logging(sys.argv[1])
    try:
        install(*sys.argv[1:])
    except Exception:
        LOG.exception("Tool installation failed")
        sys.exit(1)
