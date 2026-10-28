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

"""Select local verification without changing the full CI verification target."""

import argparse
import hashlib
import os
from pathlib import Path
import shlex
import stat
import subprocess
import sys


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args])


def changed_paths(root, base_ref):
    if not base_ref:
        for candidate in ("upstream/main", "origin/main"):
            result = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "--verify", candidate],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            if result.returncode == 0:
                base_ref = candidate
                break
        else:
            raise ValueError("Set VERIFY_BASE_REF to the PR's target branch; no remote main ref was found")
    base = git(root, "rev-parse", "--verify", "--end-of-options", base_ref + "^{commit}").decode().strip()
    merge_base = git(root, "merge-base", base, "HEAD").decode().strip()
    paths = set()
    for args in (
        ("diff", "--no-renames", "--name-only", "-z", merge_base, "HEAD", "--"),
        ("diff", "--no-renames", "--name-only", "-z", "HEAD", "--"),
        ("ls-files", "--others", "--exclude-standard", "-z"),
    ):
        paths.update(os.fsdecode(p) for p in git(root, *args).split(b"\0") if p)
    # Git lists a nested worktree as a directory, not as files in this checkout.
    paths = {p for p in paths if not (root / p).is_dir()}
    return sorted(paths), base_ref


def cli_dependencies(root, go_command, platforms):
    directories = set()
    for platform in platforms.split(","):
        parts = platform.split("/")
        if len(parts) != 2 or not all(parts):
            raise ValueError("CLI_PLATFORMS must contain os/architecture pairs")
        env = dict(os.environ, GOOS=parts[0], GOARCH=parts[1], CGO_ENABLED="0")
        output = subprocess.check_output(
            shlex.split(go_command) + ["list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", "./cmd/kueuectl"],
            cwd=root, text=True, env=env,
        )
        for line in output.splitlines():
            if not line:
                continue
            try:
                directories.add(Path(line).resolve().relative_to(root).as_posix())
            except ValueError:
                pass  # Module-cache dependencies are covered by go.mod/go.sum.

    return directories


def select_targets(root, paths, go_command, platforms):
    generators, checks, fallback = set(), set(), []
    production_go = [p for p in paths if p.endswith(".go") and not p.endswith("_test.go")]
    cli = any(p in ("LICENSE", "hack/multiplatform-build.sh") for p in paths)
    if production_go:
        try:
            dependencies = cli_dependencies(root, go_command, platforms)
            cli |= any(str(Path(p).parent) in dependencies for p in production_go)
        except (OSError, subprocess.CalledProcessError):
            print("Could not resolve CLI dependencies; including verify-cli conservatively", file=sys.stderr)
            cli = True

    deployment_checks = {
        "verify-helm", "kustomize-build-verify",
        "rbac-verify", "rbac-role-coverage-verify", "kustomization-resources-verify",
    }
    for path in paths:
        name = Path(path).name
        if path == "Makefile" or path.startswith(("hack/make/", "hack/tools/")) or name in ("go.mod", "go.sum", "go.work", "go.work.sum", ".go-version"):
            fallback.append(path)
        elif path.startswith("cmd/experimental/skills/") or name in ("AGENTS.md", "CLAUDE.md", "GEMINI.md"):
            checks.add("verify-skills-lint")
        elif path.startswith(("charts/", "config/", "cmd/experimental/kueue-populator/charts/", "cmd/experimental/kueue-priority-booster/charts/")) or path == "hack/processing-plan.yaml":
            generators.add("verify-manifests")
            checks.update(deployment_checks)
        elif path.startswith("hack/genref/"):
            generators.add("generate-apiref")
        elif path.startswith(("cmd/kueueviz/frontend/", "test/e2e/kueueviz/")):
            checks.add("npm-depcheck")
        elif path.startswith("cmd/kueuectl-docs/"):
            generators.add("generate-kueuectl-docs")
            if path.endswith(".go"):
                checks.add("verify-go")
        elif path.endswith(".go"):
            checks.add("verify-go")
            if not path.endswith("_test.go"):
                if path.startswith("apis/"):
                    generators.update({"verify-tree-prereqs", "verify-manifests"})
                    checks.add("lint-api")
                    checks.update(deployment_checks)
                elif path.startswith("client-go/"):
                    generators.add("generate-code")
                elif path.startswith(("internal/mocks/", "pkg/controller/jobframework/", "pkg/controller/core/")):
                    generators.add("generate-mocks")
                if path.startswith(("pkg/controller/", "pkg/webhooks/", "pkg/util/cert/", "pkg/visibility/", "pkg/config/")):
                    generators.add("verify-manifests")
                    checks.update(deployment_checks)
                if path.startswith(("pkg/metrics/", "hack/tools/metricsdoc/")):
                    generators.add("generate-metrics-tables")
                if path.startswith("pkg/features/"):
                    generators.add("generate-featuregates")
        elif path.startswith("hack/testing/shellcheck/") or name == ".shellcheckrc":
            checks.add("shell-lint")
        elif path.endswith(".sh"):
            checks.add("shell-lint")
            if path.startswith("hack/testing/"):
                checks.update({"e2e-common-test", "milestone-pull-test", "verify-generation-order", "verify-make-failure-summary", "verify-unit-test-selection"})
            elif path.startswith("hack/") and path != "hack/multiplatform-build.sh":
                fallback.append(path)  # Build/generation scripts may affect any output.
        elif path.startswith("hack/releasing/"):
            checks.add("release-utils-test")
        elif path.startswith(".golangci"):
            checks.update({"verify-go", "lint-api"})
        elif path.endswith(".md") or path.startswith("site/") or path == "netlify.toml":
            generators.add("verify-docs")
        elif path == "LICENSE":
            pass  # Included in the CLI packages below.
        else:
            fallback.append(path)

    if fallback:
        return [], ["verify-checks"], fallback
    if cli:
        checks.add("verify-cli")
        generators.add("generate-kueuectl-docs")
    return sorted(generators), sorted(checks), []


def tree_snapshot(root, paths):
    if not paths:
        return {}
    files = git(root, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", *paths)
    snapshot = {}
    for raw in files.split(b"\0"):
        if not raw:
            continue
        name = os.fsdecode(raw)
        path = root / name
        try:
            mode = path.lstat().st_mode
            if stat.S_ISLNK(mode):
                contents = os.fsencode(os.readlink(path))
            elif stat.S_ISREG(mode):
                contents = path.read_bytes()
            else:
                continue
            snapshot[name] = (stat.S_IMODE(mode), hashlib.sha256(contents).hexdigest())
        except FileNotFoundError:
            snapshot[name] = None
    return snapshot


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-ref", default="")
    parser.add_argument("--go", default="go")
    parser.add_argument("--make", default="make")
    parser.add_argument("--cli-platforms", default="linux/amd64,linux/arm64,darwin/amd64,darwin/arm64")
    parser.add_argument("--jobs", type=int, default=8)
    parser.add_argument("--plan", action="store_true")
    parser.add_argument("--paths", nargs="*", default=[])
    args = parser.parse_args()
    if args.jobs < 1:
        parser.error("--jobs must be positive")
    root = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()
    paths, base_ref = changed_paths(root, args.base_ref)
    generators, checks, fallback = select_targets(root, paths, args.go, args.cli_platforms)
    print("Verification base:", base_ref, flush=True)
    print("Changed files:", len(paths), flush=True)
    print("Generators:", " ".join(generators) or "(none)", flush=True)
    print("Checks:", " ".join(checks) or "(none)", flush=True)
    if fallback:
        print("Full verification checks selected for:", ", ".join(fallback), flush=True)
    if args.plan or not paths:
        return 0

    before = tree_snapshot(root, args.paths)
    # Separate phases prevent lint/build readers racing generators. Keep each
    # phase in one Make invocation so shared prerequisites run only once.
    for targets in (generators, checks):
        if targets:
            result = subprocess.run(shlex.split(args.make) + ["-j", str(args.jobs), *targets], cwd=root)
            if result.returncode:
                return result.returncode
    after = tree_snapshot(root, args.paths)
    changed = sorted(name for name in before.keys() | after.keys() if before.get(name) != after.get(name))
    if changed:
        print("Verification updated generated/output files; review them and rerun:", file=sys.stderr)
        for name in changed:
            print("  " + name, file=sys.stderr)
        return 1
    print("Selected verification passed; generated/output files stayed unchanged", flush=True)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print("Verification selection failed: " + str(error), file=sys.stderr)
        sys.exit(1)
