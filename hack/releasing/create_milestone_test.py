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

"""Exercise the create-milestone action against milestone_pull.sh with simulated GitHub responses.

Requires bash, jq and the dependencies in requirements.txt.
"""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
ACTION_PATH = REPO_ROOT / ".github" / "actions" / "create-milestone" / "action.yml"

GH_STUB = """#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$GH_LOG"
case "$*" in
  *"milestones?state=all"*)
    printf '%s' "$GH_MILESTONES"
    ;;
  *"--method POST"*)
    if [[ -n "${GH_FAIL_CREATE:-}" ]]; then
      echo "HTTP 403: Resource not accessible by integration" >&2
      exit 1
    fi
    ;;
esac
"""


class CreateMilestoneActionTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        bin_dir = root / "bin"
        bin_dir.mkdir()
        gh = bin_dir / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(0o755)

        action = yaml.safe_load(ACTION_PATH.read_text())
        step = next(step for step in action["runs"]["steps"] if step.get("id") == "create")
        self.script = root / "create.sh"
        self.script.write_text(step["run"])

        self.gh_log = root / "gh.log"
        self.output = root / "output"
        self.env = {
            **{k: v for k, v in os.environ.items() if k not in ("CREATE_MILESTONE", "SKIP_PR", "DRY_RUN")},
            "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
            "GH_LOG": str(self.gh_log),
            "GITHUB_OUTPUT": str(self.output),
            "TARGET_REPO": "owner/repo",
            "VERSION": "v0.21.0",
        }

    def run_action(self, milestones=(), **env):
        self.gh_log.write_text("")
        self.output.write_text("")
        result = subprocess.run(
            ["bash", "-e", "-o", "pipefail", str(self.script)],
            cwd=REPO_ROOT,
            env={**self.env, "GH_MILESTONES": json.dumps(list(milestones)), **env},
            capture_output=True,
            text=True,
            check=False,
        )
        return result, self.output.read_text(), self.gh_log.read_text()

    def test_creates_an_absent_milestone(self):
        result, output, gh_log = self.run_action([{"title": "v0.21", "state": "closed"}])
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertEqual("message=✅ Milestone `v0.22` has been successfully created.\n", output)
        self.assertIn("api --method POST repos/owner/repo/milestones -f title=v0.22", gh_log)

    def test_existing_open_milestone_is_left_alone(self):
        result, output, gh_log = self.run_action([{"title": "v0.22", "state": "open"}])
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertEqual("message=✅ Milestone `v0.22` already exists.\n", output)
        self.assertNotIn("POST", gh_log)

    def test_existing_closed_milestone_is_left_closed(self):
        result, output, gh_log = self.run_action([{"title": "v0.22", "state": "closed"}])
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertEqual("message=✅ Milestone `v0.22` already exists (closed, left as-is).\n", output)
        self.assertNotIn("POST", gh_log)

    def test_patch_release_fails_before_calling_github(self):
        result, output, gh_log = self.run_action(VERSION="v0.21.1")
        self.assertNotEqual(0, result.returncode)
        self.assertTrue(output.startswith("message=❌ v0.21.1 is a patch release."), output)
        self.assertEqual("", gh_log)

    def test_failed_creation_is_reported(self):
        result, output, _ = self.run_action(GH_FAIL_CREATE="1")
        self.assertNotEqual(0, result.returncode)
        self.assertEqual("message=❌ Failed to create milestone `v0.22`.\n", output)


if __name__ == "__main__":
    unittest.main()
