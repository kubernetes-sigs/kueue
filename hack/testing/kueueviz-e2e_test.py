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
    def run_suite(self, script="backend", failure=""):
        """Run the real scripts with recording substitutes for external services."""
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            scripts = root / "hack/testing"
            scripts.mkdir(parents=True)
            for name in ("backend", "local"):
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
# Synchronize an early process exit before the actual termination attempt, so
# testing a failed kill does not depend on the backend's scheduling.
kill() {
    if [[ "$VIZ_TEST_FAILURE" == backend_already_* && "$1" != -0 ]]; then
        builtin kill "$@" 2>/dev/null || true
        wait "$1" 2>/dev/null || true
    fi
    builtin kill "$@"
}
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
def wait_for_backend():
    with ready.open() as pipe:
        assert pipe.read() == "ready\\n"
if name == "go":
    binary = pathlib.Path("bin/kueueviz")
    binary.write_text(pathlib.Path(os.environ["VIZ_TEST_BACKEND_TEMPLATE"]).read_text())
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
''')
            stub.chmod(0o755)
            for name in ("go", "docker", "jq", "kubectl", "npm"):
                (root / "bin" / name).symlink_to(stub)
            events = root / "events"
            commands = root / "commands"
            ready = root / "backend.ready"
            os.mkfifo(ready)
            backend = root / "backend-template"
            backend.write_text('''#!/usr/bin/env python3
import os, pathlib, signal, sys
failure = os.environ["VIZ_TEST_FAILURE"]
status = 66 if failure in ("backend_race", "backend_already_exited") else 0
signal.signal(signal.SIGTERM, lambda *_: sys.exit(status))
pathlib.Path(os.environ["VIZ_TEST_BACKEND_PID"]).write_text(str(os.getpid()))
with open(os.environ["VIZ_TEST_BACKEND_READY"], "w") as pipe:
    pipe.write("ready\\n")
signal.pause()
''')
            env = {**os.environ, "PATH": f"{root / 'bin'}:{os.environ['PATH']}",
                   "ARTIFACTS": str(root / "artifacts"), "KIND_CLUSTER_NAME": "test",
                   "KIND_CLUSTER_FILE": "kind-cluster.yaml", "PROJECT_DIR": str(root),
                   "CYPRESS_IMAGE_NAME": "cypress-test", "VIZ_TEST_EVENTS": str(events),
                   "VIZ_TEST_BACKEND_PID": str(root / "backend.pid"),
                   "VIZ_TEST_BACKEND_READY": str(ready),
                   "VIZ_TEST_BACKEND_TEMPLATE": str(backend),
                   "VIZ_TEST_COMMANDS": str(commands), "VIZ_TEST_FAILURE": failure}
            process = subprocess.Popen(
                ["bash", str(scripts / f"e2e-kueueviz-{script}.sh")],
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

    def test_suites_preserve_cluster_lifecycle_and_run_cypress(self):
        for script in ("backend", "local"):
            for failure in ("", "backend_already_stopped"):
                with self.subTest(script=script, failure=failure):
                    result, events, commands = self.run_suite(script, failure=failure)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(events, ["create", "prepare", "load", "deploy", "artifacts", "cleanup"])
                    expected = ["docker", "run"] if script == "backend" else ["npm", "run", "cypress:run"]
                    self.assertTrue(any(c[:len(expected)] == expected for c in commands))

    def test_failures_propagate_and_owner_collects_artifacts_and_cleans_up(self):
        for script in ("backend", "local"):
            for failure in ("create", "cypress", "backend_exit", "backend_race", "backend_already_exited"):
                with self.subTest(script=script, failure=failure):
                    result, events, _ = self.run_suite(script, failure=failure)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    if failure in ("cypress", "backend_exit"):
                        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
                    if failure in ("backend_race", "backend_already_exited"):
                        self.assertEqual(result.returncode, 66, result.stdout + result.stderr)
                    self.assertEqual(events.count("create"), 1)
                    self.assertEqual(events[-2:], ["artifacts", "cleanup"])
                    self.assertEqual(events.count("cleanup"), 1)

    def test_make_preserves_build_flags_and_reuses_only_the_race_test_flag(self):
        cases = (("", None, "-race"), ("-trimpath", "-race -count=1", "-trimpath -race"),
                 ("-trimpath", "-count=1", "-trimpath"), ("", "", ""),
                 ('"-ldflags=-s -w"', "-race", '"-ldflags=-s -w" -race'))
        for go_flags, test_flags, expected in cases:
            with self.subTest(go_flags=go_flags, test_flags=test_flags):
                with tempfile.TemporaryDirectory() as temp:
                    root = Path(temp)
                    script = root / "hack/testing/e2e-kueueviz-backend.sh"
                    script.parent.mkdir(parents=True)
                    script.write_text('#!/bin/sh\nprintf "%s" "$KUEUEVIZ_BACKEND_GOFLAGS" > "$PROJECT_DIR/build-flags"\n')
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
