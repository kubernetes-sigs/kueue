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
        for failure in ("", "certmanager", "compile", "cluster", "load", "deploy-manager", "deploy-worker1", "deploy-worker2", "run"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                scripts = root / "hack/testing"
                scripts.mkdir(parents=True)
                shutil.copy(ROOT / "hack/testing/e2e-multikueue-test.sh", scripts)
                (scripts / "multikueue").mkdir()
                (scripts / "multikueue/manager-cluster.kind.yaml").touch()
                suite = root / "test/e2e/multikueue/baseline"
                suite.mkdir(parents=True)
                os.mkfifo(root / "compile-release")
                os.mkfifo(root / "deploy-manager")
                os.mkfifo(root / "deploy-worker1")
                common = (ROOT / "hack/testing/e2e-common.sh").read_text()
                timing = common[common.index("function e2e_timed {"):common.index("# Returns success when a failed `docker pull`")]
                (scripts / "e2e-common.sh").write_text(timing + r'''
KIND_VERSION=1.36.4
KIND=true
GINKGO=compile_suite
if [[ "$FAIL" == certmanager ]]; then
    CERTMANAGER_VERSION=test
    E2E_USE_HELM=false
fi
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
    if [[ -z "$FAIL" || "$FAIL" == certmanager ]]; then
        # Deployment must release compilation, proving neither setup barrier waits for it.
        read -r _ < compile-release
    fi
    touch test/e2e/multikueue/baseline/baseline.test
    echo compiled >> events
}
cluster_kueue_deploy() {
    local cluster=${1##*kind-}
    [[ "$FAIL" != "deploy-$cluster" ]] || return 19
    if [[ "$FAIL" == certmanager ]]; then
        case "$cluster" in
            worker1) [[ -f manager-deployed ]] ;;
            worker2) [[ -f worker1-deployed ]] ;;
        esac
        touch "$cluster-deployed"
    fi
    # All three deployments must start before the manager can finish.
    if [[ -z "$FAIL" ]]; then
        case "$cluster" in
            manager) read -r _ < deploy-manager ;;
            worker1) read -r _ < deploy-worker1; echo ready > deploy-manager ;;
            worker2) echo ready > deploy-worker1 ;;
        esac
    fi
    echo "deployed $cluster" >> events
    if [[ ( -z "$FAIL" || "$FAIL" == certmanager ) && "$1" == *kind-worker2 ]]; then
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
                succeeds = failure in ("", "certmanager")
                self.assertEqual(result.returncode == 0, succeeds, result.stdout + result.stderr)
                self.assertEqual(sum(line.startswith("cleanup ") for line in events), 3)
                runs = [line for line in events if line.startswith("run ")]
                if failure in ("compile", "cluster", "load") or failure.startswith("deploy-"):
                    self.assertEqual(runs, [])
                else:
                    self.assertEqual(len(runs), 1)
                    self.assertIn("./test/e2e/multikueue/baseline/...", runs[0])
                    self.assertNotIn(".test", runs[0])
                    self.assertLess(events.index("compiled"), events.index(runs[0]))
                if succeeds:
                    self.assertLess(events.index("deployed"), events.index("compiled"))
                    self.assertIn("report", events)
                else:
                    self.assertNotIn("report", events)
                for event in events:
                    if event.startswith("deployed "):
                        self.assertLess(events.index(event), events.index("restored"))


if __name__ == "__main__":
    unittest.main()
