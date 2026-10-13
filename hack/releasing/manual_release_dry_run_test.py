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

"""Check dry-run behavior even when matching release PRs already exist."""

import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]


class ManualReleaseDryRunTest(unittest.TestCase):
    def test_dry_run_does_not_edit_existing_release_issue(self):
        scripts = {
            "prepare all": ("prepare_pull.sh", 'if [[ "$TARGET" == "all"', "all", 2),
            "prepare release": ("prepare_pull.sh", 'if [[ "$TARGET" == "all"', "release", 1),
            "prepare main": ("prepare_pull.sh", 'if [[ "$TARGET" == "all"', "main", 1),
            "promote": ("promote_pull.sh", 'K8S_IO_BRANCH="', "all", 1),
            "CI": ("ci_pull.sh", 'CI_BRANCH="', "all", 1),
        }
        for name, (script_name, start, target, phases) in scripts.items():
            source = (REPO_ROOT / "hack/releasing" / script_name).read_text()
            push = re.search(r"^function push_and_create_pr\(\) \{.*?^\}",
                             source, re.MULTILINE | re.DOTALL).group()
            script = push + "\n" + source[source.index(start):]
            for dry_run in ("", "1"):
                with self.subTest(script=name, dry_run=dry_run), tempfile.TemporaryDirectory() as directory:
                    calls = Path(directory) / "calls"
                    env = {
                        **os.environ,
                        "CALLS": str(calls), "DRY_RUN": dry_run, "TARGET": target, "FORK_REMOTE": "origin",
                        "RELEASE_VERSION": "v0.20.0", "RELEASE_BRANCH": "release-0.20", "MAJOR_MINOR": "0.20",
                        "RELEASE_ISSUE_NUMBER": "1", "RELEASE_ISSUE_NAME": "Release v0.20.0",
                        "RELEASE_ISSUE_BODY": "<!-- PREPARE_PULL_RELEASE -->\n<!-- PREPARE_PULL_MAIN -->\n<!-- K8S_IO_PULL -->\n<!-- CI_PULL -->",
                        "MAIN_REPO_ORG": "kubernetes-sigs", "MAIN_REPO_NAME": "kueue",
                        "KUBERNETES_SIGS_KUEUE_MAIN_REPO": "kubernetes-sigs/kueue",
                        "KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG": "kubernetes-sigs",
                        "KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME": "kueue",
                        "KUBERNETES_TEST_INFRA_MAIN_REPO_ORG": "kubernetes",
                        "KUBERNETES_TEST_INFRA_MAIN_REPO_NAME": "test-infra",
                        "KUBERNETES_TEST_INFRA_FORK_REMOTE": "origin",
                        "KUBERNETES_K8S_IO_MAIN_REPO": "kubernetes/k8s.io",
                        "KUBERNETES_K8S_IO_FORK_REMOTE": "origin",
                    }
                    harness = '''
set -o errexit
set -o nounset
set -o pipefail
function prepare_local_branch() { :; }
function git() { echo "git push" >> "$CALLS"; }
function make_pr() { gh pr create; }
function gh() {
  echo "$1 $2" >> "$CALLS"
  if [[ "$1 $2" == "pr list" ]]; then
    printf '42\t%s\n' "Prepare release v0.20.0" "Update main with the latest v0.20.0" "Kueue: Promote v0.20.0" "Kueue: CI for 0.20"
  fi
}
'''
                    result = subprocess.run(["bash", "-c", harness + script], env=env, cwd=directory,
                                            input="y\ny\n", capture_output=True, text=True, check=False)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    recorded = calls.read_text().splitlines() if calls.exists() else []
                    expected = [] if dry_run else ["git push", "pr create", "pr list", "issue edit"] * phases
                    self.assertEqual(recorded, expected)


if __name__ == "__main__":
    unittest.main()
