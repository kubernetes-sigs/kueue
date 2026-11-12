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
    def run_suite(self, script="backend", args=(), failure="", backend_env=None):
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
cluster_create() {
    record create
    if [[ "$VIZ_TEST_SCRIPT" == backend ]]; then
        # Prove frontend preparation starts before cluster preparation finishes.
        read -r frontend_started < "$VIZ_TEST_FRONTEND_STARTED"
        [[ "$frontend_started" == started ]] || return 1
    fi
    [[ "$VIZ_TEST_FAILURE" != create ]]
}
prepare_kueue_image() { record prepare; [[ "$VIZ_TEST_FAILURE" != prepare ]]; }
cluster_kind_load_kueue_image() { record load; [[ "$VIZ_TEST_FAILURE" != load ]]; }
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
elif name == "docker" and args[0] == "create":
    sys.exit(7 if failure == "frontend_create" else 0)
elif name == "docker" and args[0] == "rm":
    frontend_pid = pathlib.Path(os.environ["VIZ_TEST_FRONTEND_PID"])
    if frontend_pid.exists():
        try:
            os.kill(int(frontend_pid.read_text()), signal.SIGTERM)
        except ProcessLookupError:
            pass
elif name == "docker" and args[0] == "exec":
    if failure in ("frontend_start", "frontend_signal"):
        sys.exit(1)
    with open(os.environ["VIZ_TEST_FRONTEND_RELEASE"], "w") as pipe:
        pipe.write("started\\n")
elif name == "docker" and args[0] == "inspect":
    print(7 if failure == "frontend_start" else 0)
elif name == "docker" and args[0] == "start":
    pathlib.Path(os.environ["VIZ_TEST_FRONTEND_PID"]).write_text(str(os.getpid()))
    with open(os.environ["VIZ_TEST_FRONTEND_STARTED"], "w") as pipe:
        pipe.write("started\\n")
    if failure == "frontend_start":
        sys.exit(7)
    wait_for_backend()
    with open(os.environ["VIZ_TEST_FRONTEND_RELEASE"]) as pipe:
        assert pipe.read() == "started\\n"
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
            frontend_started = root / "frontend.started"
            os.mkfifo(frontend_started)
            frontend_release = root / "frontend.release"
            os.mkfifo(frontend_release)
            backend = root / "backend-template"
            backend.write_text('''#!/usr/bin/env python3
import os, pathlib, signal, sys
failure = os.environ["VIZ_TEST_FAILURE"]
is_rbac = os.environ.get("KUEUEVIZ_AUTH_MODE") == "TokenReview"
race_failures = ("rbac_race",) if is_rbac else ("backend_race", "backend_already_exited")
status = 66 if failure in race_failures else 0
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
                   "VIZ_TEST_SCRIPT": script,
                   "VIZ_TEST_FRONTEND_PID": str(root / "frontend.pid"),
                   "VIZ_TEST_FRONTEND_STARTED": str(frontend_started),
                   "VIZ_TEST_FRONTEND_RELEASE": str(frontend_release),
                   "VIZ_TEST_BACKEND_PID": str(root / "backend.pid"),
                   "VIZ_TEST_BACKEND_READY": str(ready),
                   "VIZ_TEST_BACKEND_TEMPLATE": str(backend),
                   "VIZ_TEST_COMMANDS": str(commands), "VIZ_TEST_FAILURE": failure}
            env.update(backend_env or {})
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
            for failure in ("", "backend_already_stopped"):
                with self.subTest(script=script, failure=failure):
                    result, events, commands = self.run_suite(script, failure=failure)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(events, ["create", "prepare", "load", "deploy", "artifacts", "cleanup"])
                    self.assertIn("SECURE: the backend denied", result.stdout)
                    self.assertTrue(any(c[:3] == ["kubectl", "auth", "can-i"] for c in commands))
                    self.assertTrue(any(c[:3] == ["kubectl", "get", "pod"] and
                                        "--kubeconfig=/dev/null" in c for c in commands))
                    self.assertTrue(any(c[0] == "curl" and "Authorization: Bearer test-token" in c
                                        for c in commands))
                    if script == "backend":
                        self.assertTrue(any(c[:3] == ["docker", "start", "--attach"] for c in commands))
                        self.assertEqual(sum(c[:3] == ["docker", "rm", "-f"] for c in commands), 1)

    def test_frontend_container_failures_stop_the_suite_and_clean_up(self):
        for failure in ("frontend_create", "frontend_start", "frontend_signal"):
            with self.subTest(failure=failure):
                result, events, commands = self.run_suite(failure=failure)
                self.assertEqual(result.returncode, 1 if failure == "frontend_signal" else 7,
                                 result.stdout + result.stderr)
                self.assertNotIn("SECURE:", result.stdout)
                self.assertEqual(sum(c[:3] == ["docker", "rm", "-f"] for c in commands), 1)
                if failure == "frontend_create":
                    self.assertEqual(events, [])
                else:
                    self.assertEqual(events.count("create"), 1)
                    self.assertEqual(events[-2:], ["artifacts", "cleanup"])

    def test_frontend_health_address_matches_backend_overrides(self):
        cases = (
            ({}, "http://localhost:8080/healthz"),
            ({"KUEUEVIZ_PORT": "8181"}, "http://localhost:8181/healthz"),
            ({"KUEUEVIZ_LISTEN": "8182", "KUEUEVIZ_PORT": "9999"}, "http://localhost:8182/healthz"),
            ({"KUEUEVIZ_LISTEN": "localhost", "KUEUEVIZ_PORT": "9999"}, "http://localhost:8080/healthz"),
            ({"KUEUEVIZ_LISTEN": "0.0.0.0", "KUEUEVIZ_PORT": "9999"}, "http://127.0.0.1:8080/healthz"),
            ({"KUEUEVIZ_LISTEN": "0.0.0.0:8182"}, "http://127.0.0.1:8182/healthz"),
            ({"KUEUEVIZ_LISTEN": "[::]:8183"}, "http://[::1]:8183/healthz"),
            ({"KUEUEVIZ_LISTEN": "127.0.0.2:8184"}, "http://127.0.0.2:8184/healthz"),
            ({"KUEUEVIZ_HEALTH_URL": "http://example.test/ready"}, "http://example.test/ready"),
        )
        for overrides, expected in cases:
            with self.subTest(overrides=overrides):
                env = {"KUEUEVIZ_PORT": "", "KUEUEVIZ_LISTEN": "", "KUEUEVIZ_HEALTH_URL": "", **overrides}
                result, _, commands = self.run_suite(backend_env=env)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                create = next(c for c in commands if c[:2] == ["docker", "create"])
                self.assertIn(f"KUEUEVIZ_HEALTH_URL={expected}", create)

    def test_failures_propagate_and_owner_collects_artifacts_and_cleans_up(self):
        for script in ("backend", "local"):
            for failure in ("create", "prepare", "load", "cypress", "rbac", "backend_exit", "backend_race", "rbac_race", "backend_already_exited"):
                with self.subTest(script=script, failure=failure):
                    result, events, _ = self.run_suite(script, failure=failure)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    if failure in ("cypress", "backend_exit"):
                        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
                    if failure in ("backend_race", "rbac_race", "backend_already_exited"):
                        self.assertEqual(result.returncode, 66, result.stdout + result.stderr)
                    self.assertEqual(events.count("create"), 1)
                    self.assertEqual(events[-2:], ["artifacts", "cleanup"])
                    self.assertEqual(events.count("cleanup"), 1)
                    if failure != "rbac_race":
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

    def test_frontend_waits_for_backend_and_preserves_failures(self):
        for failure in ("", "unit", "health", "cypress", "slow_setup", "transient_health"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                for directory in ("hack/testing", "cmd/kueueviz/frontend", "test/e2e/kueueviz", "bin"):
                    (root / directory).mkdir(parents=True)
                script = root / "hack/testing/e2e-kueueviz-frontend.sh"
                shutil.copy2(ROOT / "hack/testing/e2e-kueueviz-frontend.sh", script)
                retry = root / "hack/testing/retry.sh"
                shutil.copy2(ROOT / "hack/testing/retry.sh", retry)
                stub = root / "bin/stub"
                stub.write_text('''#!/usr/bin/env python3
import json, os, pathlib, signal, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["VIZ_TEST_COMMANDS"], "a") as log:
    log.write(json.dumps([name, *args]) + "\\n")
failure = os.environ["VIZ_TEST_FAILURE"]
ready = pathlib.Path(os.environ["KUEUEVIZ_BACKEND_START_FILE"])
def count(name):
    counter = ready.with_suffix(name)
    value = int(counter.read_text()) + 1 if counter.exists() else 1
    counter.write_text(str(value))
    return value
if name == "sleep":
    # Simulate cluster preparation taking longer than the health-retry budget,
    # without a wall-clock wait. Health probing must start only after the marker.
    if failure == "slow_setup" and count(".sleeps") == 130:
        ready.touch()
if name == "curl":
    assert ready.exists(), "Health deadline started before cluster preparation completed"
    assert args[-1] == os.environ["KUEUEVIZ_HEALTH_URL"]
    if failure == "transient_health" and count(".requests") < 3:
        sys.exit(7)
if name == "npm" and args == ["run", "dev"]:
    signal.pause()
if (name == "npm" and args == ["test"] and failure == "unit" or
    name == "curl" and failure == "health" or
    name == "npm" and args[:2] == ["run", "cypress:run"] and failure == "cypress"):
    sys.exit(7)
''')
                stub.chmod(0o755)
                for name in ("apt-get", "npm", "npx", "curl", "sleep"):
                    (root / "bin" / name).symlink_to(stub)
                commands_file = root / "commands"
                ready_file = root / "backend-started"
                if failure != "slow_setup":
                    ready_file.touch()
                env = {**os.environ, "PATH": f"{root / 'bin'}:{os.environ['PATH']}",
                       "PROJECT_DIR": str(root), "VIZ_TEST_COMMANDS": str(commands_file),
                       "KUEUEVIZ_BACKEND_START_FILE": str(ready_file),
                       "KUEUEVIZ_HEALTH_URL": "http://127.0.0.1:8181/healthz",
                       "VIZ_TEST_FAILURE": failure}
                process = subprocess.Popen(["bash", str(script)], cwd=root, env=env,
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                           text=True, start_new_session=True)
                try:
                    stdout, stderr = process.communicate(timeout=30)
                finally:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.wait()
                expected_status = {"unit": 7, "health": 1, "cypress": 7}.get(failure, 0)
                self.assertEqual(process.returncode, expected_status, stdout + stderr)
                commands = [json.loads(line) for line in commands_file.read_text().splitlines()]
                unit = commands.index(["npm", "test"])
                health = [i for i, c in enumerate(commands) if c[0] == "curl"]
                cypress = [i for i, c in enumerate(commands) if c[:3] == ["npm", "run", "cypress:run"]]
                if failure == "unit":
                    self.assertEqual(health, [])
                else:
                    self.assertEqual(len(health), {"health": 120, "transient_health": 3}.get(failure, 1))
                    self.assertGreater(health[0], unit)
                    if failure == "slow_setup":
                        self.assertEqual(sum(c[0] == "sleep" for c in commands[:health[0]]), 130)
                if failure in ("unit", "health"):
                    self.assertEqual(cypress, [])
                else:
                    self.assertEqual(len(cypress), 1)
                    self.assertGreater(cypress[0], health[0])


if __name__ == "__main__":
    unittest.main()
