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

import contextlib
import io
import os
import unittest
from pathlib import Path
from unittest import mock

import yaml

import log_to_issue

REPO_ROOT = Path(__file__).resolve().parents[2]
TEMPLATE_PATH = REPO_ROOT / ".github" / "ISSUE_TEMPLATE" / "NEW_RELEASE.md"
WORKFLOW_PATH = REPO_ROOT / ".github" / "workflows" / "release-utils.yml"


def checked_lines(body: str) -> list[str]:
    return [line for line in body.splitlines() if "- [x]" in line]


class WorkflowTest(unittest.TestCase):
    def test_only_success_reports_mark_done(self) -> None:
        jobs = yaml.safe_load(WORKFLOW_PATH.read_text())["jobs"]
        for job_name, job in jobs.items():
            for step in job.get("steps", []):
                if not str(step.get("uses", "")).endswith("report-result"):
                    continue
                with self.subTest(job=job_name, step=step["name"]):
                    mark_done = str(step.get("with", {}).get("mark-done", "false")).lower() == "true"
                    self.assertEqual(mark_done, step["name"] == "Report Success")


class MainTest(unittest.TestCase):
    def run_main(self, env: dict[str,str]) -> str:
        base_env = {
            "GITHUB_ACTOR": "release-manager",
            "GITHUB_RUN_ID": "1",
            "GITHUB_REPOSITORY": "kubernetes-sigs/kueue",
            "INPUT_ALIAS": "",
            "INPUT_CLEANUP": "false",
            "INPUT_COMMAND": "tag-release",
            "INPUT_MESSAGE": "✅ Release tag created.",
            "ISSUE_BODY": TEMPLATE_PATH.read_text(),
        }
        stdout = io.StringIO()
        with mock.patch.dict(os.environ, {**base_env, **env}, clear=True), contextlib.redirect_stdout(stdout):
            log_to_issue.main()
        return stdout.getvalue()

    def test_mark_done(self) -> None:
        test_cases = {
            "mark-done true checks the item": ({"INPUT_MARK_DONE": "true"}, 1),
            "mark-done not set leaves the checklist untouched": ({}, 0),
        }
        for name, (env, want_checked) in test_cases.items():
            with self.subTest(name):
                out = self.run_main(env)
                self.assertEqual(len(checked_lines(out)), want_checked)
                self.assertIn("Command: /tag-release", out)

    def test_latest_entry_is_on_top(self) -> None:
        body = self.run_main({"INPUT_COMMAND": "tag-release"})
        body = self.run_main({"INPUT_COMMAND": "publish-release", "ISSUE_BODY": body})
        self.assertLess(body.index("### Publish release"), body.index("### Tag release"))

        # Re-running a command moves its entry to the top and archives the old one in history.
        body = self.run_main({"INPUT_COMMAND": "tag-release", "ISSUE_BODY": body})
        self.assertLess(body.index("### Tag release"), body.index("### Publish release"))
        self.assertLess(body.index("### Publish release"), body.index("### History"))
        self.assertIn("<summary><b>Tag release history</b></summary>", body)
        self.assertEqual(body.count("### Tag release"), 1)


if __name__ == "__main__":
    unittest.main()
