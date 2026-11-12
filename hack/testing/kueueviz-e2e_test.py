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

"""Exercise KueueViz suite ownership and failure propagation without a cluster."""

import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class KueueVizE2ETest(unittest.TestCase):
    def run_suite(self, script="backend", args=(), failure=""):
        """Run the real scripts with recording substitutes for external services."""
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            scripts = root / "hack/testing"
            scripts.mkdir(parents=True)
            for name in ("backend", "local", "rbac-bypass"):
                filename = f"e2e-kueueviz-{name}.sh"
                shutil.copy2(ROOT / "hack/testing" / filename, scripts / filename)
            for directory in ("cmd/kueueviz/backend/bin", "cmd/kueueviz/frontend",
                              "test/e2e/kueueviz", "artifacts", "bin"):
                (root / directory).mkdir(parents=True)
            # Keep timing and cluster service boundaries, but never invoke real tools.
            (scripts / "e2e-common.sh").write_text('''
record() { printf '%s\\n' "$1" >> "$VIZ_TEST_EVENTS"; }
e2e_timed() { shift; "$@"; }
e2e_timed_log_unfinished() { :; }
cluster_create() { record create; [[ "$VIZ_TEST_FAILURE" != create ]]; }
prepare_docker_images() { record prepare; }
cluster_kind_load() { record load; }
kueue_deploy() { record deploy; }
cluster_collect_artifacts() { record artifacts; }
cluster_cleanup() { record cleanup; }
''')
            stub = root / "bin/stub"
            stub.write_text('''#!/usr/bin/env python3
import json, os, pathlib, signal, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["VIZ_TEST_COMMANDS"], "a") as log:
    log.write(json.dumps([name, *args]) + "\\n")
failure = os.environ["VIZ_TEST_FAILURE"]
ready = pathlib.Path(os.environ["VIZ_TEST_BACKEND_READY"])
acknowledged = ready.with_suffix(".ack")
def wait_for_backend():
    if not acknowledged.exists():
        with ready.open() as pipe:
            assert pipe.read() == "ready\\n"
        acknowledged.touch()
if name == "go":
    previous = pathlib.Path(os.environ["VIZ_TEST_BACKEND_PID"])
    if previous.exists():
        try:
            os.kill(int(previous.read_text()), 0)
        except ProcessLookupError:
            pass
        else:
            sys.exit("Previous backend is still running")
    binary = pathlib.Path("bin/kueueviz")
    acknowledged.unlink(missing_ok=True)
    binary.write_text('#!/bin/bash\\necho $$ > "$VIZ_TEST_BACKEND_PID"\\necho ready > "$VIZ_TEST_BACKEND_READY"\\nexec sleep 60\\n')
    binary.chmod(0o755)
elif name == "npm":
    if args[:2] == ["run", "dev"]:
        os.execlp("sleep", "sleep", "60")
    if args[:2] == ["run", "cypress:run"]:
        wait_for_backend()
        if failure == "backend_exit":
            os.kill(int(pathlib.Path(os.environ["VIZ_TEST_BACKEND_PID"]).read_text()), signal.SIGTERM)
            sys.exit(7)
        sys.exit(7 if failure == "cypress" else 0)
elif name == "docker" and args[0] == "run":
    wait_for_backend()
    if failure == "backend_exit":
        os.kill(int(pathlib.Path(os.environ["VIZ_TEST_BACKEND_PID"]).read_text()), signal.SIGTERM)
        sys.exit(7)
    sys.exit(7 if failure == "cypress" else 0)
elif name == "kubectl":
    if args[:2] == ["auth", "can-i"]:
        print("no")
    elif args[:2] == ["create", "token"]:
        print("test-token")
    elif args[:2] == ["config", "view"]:
        print("https://api.example.test")
    elif args[:2] == ["get", "pod"]:
        sys.exit(1)
    elif args[0] == "apply":
        sys.stdin.read()
elif name == "curl":
    wait_for_backend()
    if "-w" in args:
        print("500" if failure == "rbac" else "403", end="")
''')
            stub.chmod(0o755)
            for name in ("go", "docker", "jq", "kubectl", "curl", "npm"):
                (root / "bin" / name).symlink_to(stub)
            events = root / "events"
            commands = root / "commands"
            ready = root / "backend.ready"
            os.mkfifo(ready)
            env = {**os.environ, "PATH": f"{root / 'bin'}:{os.environ['PATH']}",
                   "ARTIFACTS": str(root / "artifacts"), "KIND_CLUSTER_NAME": "test",
                   "KIND_CLUSTER_FILE": "kind-cluster.yaml", "PROJECT_DIR": str(root),
                   "CYPRESS_IMAGE_NAME": "cypress-test", "VIZ_TEST_EVENTS": str(events),
                   "VIZ_TEST_BACKEND_PID": str(root / "backend.pid"),
                   "VIZ_TEST_BACKEND_READY": str(ready),
                   "VIZ_TEST_COMMANDS": str(commands), "VIZ_TEST_FAILURE": failure}
            process = subprocess.Popen(
                ["bash", str(scripts / f"e2e-kueueviz-{script}.sh"), *args],
                cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                text=True, start_new_session=True,
            )
            try:
                stdout, stderr = process.communicate(timeout=15)
                result = subprocess.CompletedProcess(process.args, process.returncode, stdout, stderr)
            finally:
                # A failed assertion or timeout must not leave simulated services behind.
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait()
            return (result, events.read_text().splitlines() if events.exists() else [],
                    [json.loads(line) for line in commands.read_text().splitlines()]
                    if commands.exists() else [])

    def test_combined_suites_share_cluster_and_preserve_rbac_assertions(self):
        for script in ("backend", "local"):
            with self.subTest(script=script):
                result, events, commands = self.run_suite(script)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(events, ["create", "prepare", "load", "deploy", "artifacts", "cleanup"])
                self.assertIn("SECURE: the backend denied", result.stdout)
                self.assertTrue(any(c[:3] == ["kubectl", "auth", "can-i"] for c in commands))
                self.assertTrue(any(c[:3] == ["kubectl", "get", "pod"] and
                                    "--kubeconfig=/dev/null" in c for c in commands))
                self.assertTrue(any(c[0] == "curl" and "Authorization: Bearer test-token" in c
                                    for c in commands))

    def test_failures_propagate_and_owner_collects_artifacts_and_cleans_up(self):
        for script in ("backend", "local"):
            for failure in ("create", "cypress", "rbac", "backend_exit"):
                with self.subTest(script=script, failure=failure):
                    result, events, _ = self.run_suite(script, failure=failure)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    if failure in ("cypress", "backend_exit"):
                        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
                    self.assertEqual(events.count("create"), 1)
                    self.assertEqual(events[-2:], ["artifacts", "cleanup"])
                    self.assertEqual(events.count("cleanup"), 1)
                    self.assertNotIn("SECURE:", result.stdout)

    def test_standalone_rbac_keeps_owning_its_cluster(self):
        result, events, _ = self.run_suite("rbac-bypass")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(events, ["create", "prepare", "load", "deploy", "artifacts", "cleanup"])

    def test_existing_cluster_rbac_leaves_lifecycle_to_caller(self):
        for failure in ("", "rbac"):
            with self.subTest(failure=failure):
                result, events, _ = self.run_suite("rbac-bypass", ("--use-existing-cluster",), failure)
                self.assertEqual(result.returncode == 0, not failure, result.stdout + result.stderr)
                self.assertEqual(events, [])

    def test_invalid_arguments_fail_before_cluster_operations(self):
        result, events, commands = self.run_suite("rbac-bypass", ("--unknown",))
        self.assertEqual(result.returncode, 2)
        self.assertEqual(events, [])
        self.assertEqual(commands, [])

    def test_make_preserves_build_flags_and_reuses_only_the_race_test_flag(self):
        cases = (("", None, "-race"), ("-trimpath", "-race -count=1", "-trimpath -race"),
                 ("-trimpath", "-count=1", "-trimpath"), ("", "", ""))
        for go_flags, test_flags, expected in cases:
            with self.subTest(go_flags=go_flags, test_flags=test_flags):
                with tempfile.TemporaryDirectory() as temp:
                    root = Path(temp)
                    script = root / "hack/testing/e2e-kueueviz-backend.sh"
                    script.parent.mkdir(parents=True)
                    script.write_text('#!/bin/sh\nprintf "%s" "$GOFLAGS" > "$PROJECT_DIR/build-flags"\n')
                    script.chmod(0o755)
                    command = ["make", "-f", str(ROOT / "hack/make/test.mk"),
                               "-o", "test-kueueviz-backend", "-o", "setup-kueueviz-e2e-env",
                               "test-e2e-kueueviz", f"PROJECT_DIR={root}", f"GOFLAGS={go_flags}"]
                    if test_flags is not None:
                        command.append(f"GO_TEST_FLAGS={test_flags}")
                    result = subprocess.run(command, cwd=root, text=True, capture_output=True, timeout=15)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual((root / "build-flags").read_text().strip(), expected)


if __name__ == "__main__":
    unittest.main()
