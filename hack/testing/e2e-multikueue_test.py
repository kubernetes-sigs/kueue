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

"""Exercise the MultiKueue runner's ordering and failures without real clusters."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class MultiKueueRunnerTest(unittest.TestCase):
    def test_compilation_overlaps_setup_and_failures_stop_tests(self):
        for failure in ("", "compile", "cluster", "load", "deploy", "run"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                scripts = root / "hack/testing"
                scripts.mkdir(parents=True)
                shutil.copy(ROOT / "hack/testing/e2e-multikueue-test.sh", scripts)
                (scripts / "multikueue").mkdir()
                (scripts / "multikueue/manager-cluster.kind.yaml").touch()
                suite = root / "test/e2e/multikueue/baseline"
                suite.mkdir(parents=True)
                (suite / "nested").mkdir()
                (suite / "removed.test").touch()
                os.mkfifo(root / "compile-release")
                common = (ROOT / "hack/testing/e2e-common.sh").read_text()
                timing = common[common.index("function e2e_timed {"):common.index("# Returns success when a failed `docker pull`")]
                (scripts / "e2e-common.sh").write_text(timing + r'''
KIND_VERSION=1.36.4
KIND=true
GINKGO=compile_suite
build_kind_node_image() { :; }
prepare_docker_images() { :; }
ensure_kind_cluster() { [[ "$FAIL" != cluster || "$1" != kind-worker1 ]]; }
kind_load() { [[ "$FAIL" != load || "$1" != kind-worker2 ]]; }
set_managers_image() { :; }
restore_managers_image() { echo restored >> events; }
cluster_collect_artifacts() { :; }
e2e_should_delete_cluster() { return 0; }
cluster_cleanup() { echo "cleanup $1" >> events; }
compile_suite() {
    [[ "$FAIL" != compile ]] || return 17
    if [[ -z "$FAIL" ]]; then
        # Deployment must release compilation, proving neither setup barrier waits for it.
        read -r _ < compile-release
    fi
    touch test/e2e/multikueue/baseline/baseline.test
    touch test/e2e/multikueue/baseline/nested/nested.test
    echo compiled >> events
}
cluster_kueue_deploy() {
    [[ "$FAIL" != deploy ]] || return 19
    if [[ -z "$FAIL" && "$1" == *kind-worker2 ]]; then
        echo deployed >> events
        echo ready > compile-release
    fi
}
run_e2e_ginkgo() {
    echo "run $*" >> events
    [[ -f test/e2e/multikueue/baseline/baseline.test ]]
    [[ "$FAIL" != run ]]
}
''')
                binary = root / "ginkgo-top"
                binary.write_text("#!/usr/bin/env bash\necho report >> events\n")
                binary.chmod(0o755)
                # A bounded subprocess makes a broken ordering fail instead of hanging CI.
                result = subprocess.run(
                    ["bash", str(scripts / "e2e-multikueue-test.sh")],
                    cwd=root, env={**os.environ, "FAIL": failure,
                                   "KIND_CLUSTER_NAME": "kind", "ARTIFACTS": str(root / "artifacts"),
                                   "E2E_BIN_DIR": str(root),
                                   "E2E_TARGET_FOLDER": "multikueue/baseline"},
                    capture_output=True, text=True, timeout=15,
                )
                events = (root / "events").read_text().splitlines()
                self.assertEqual(result.returncode == 0, not failure, result.stdout + result.stderr)
                self.assertEqual(sum(line.startswith("cleanup ") for line in events), 3)
                runs = [line for line in events if line.startswith("run ")]
                if failure in ("compile", "cluster", "load", "deploy"):
                    self.assertEqual(runs, [])
                else:
                    self.assertEqual(len(runs), 1)
                    self.assertIn("./test/e2e/multikueue/baseline/...", runs[0])
                    self.assertNotIn(".test", runs[0])
                    self.assertLess(events.index("compiled"), events.index(runs[0]))
                if not failure:
                    self.assertLess(events.index("deployed"), events.index("compiled"))
                    self.assertIn("report", events)
                else:
                    self.assertNotIn("report", events)


if __name__ == "__main__":
    unittest.main()
