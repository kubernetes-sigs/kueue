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

    def job_steps(self, job_name: str) -> dict[str, dict]:
        job = yaml.safe_load(WORKFLOW_PATH.read_text())["jobs"][job_name]
        return {step["name"]: step for step in job["steps"]}

    def test_create_draft_release_failure_names_the_failed_step(self) -> None:
        steps = self.job_steps("create-draft-release")
        self.assertEqual(steps["Generate OpenVEX"].get("id"), "openvex")
        self.assertEqual(steps["Generate SBOM"].get("id"), "sbom")
        self.assertIn("steps.draft.outputs.message", steps["Report Success"]["with"]["message"])

        failure = steps["Report Failure"]
        self.assertEqual(failure["if"], "${{ failure() }}")
        message = failure["with"]["message"]
        self.assertLess(message.index("steps.sbom.outcome == 'failure'"), message.index("steps.openvex.outcome == 'failure'"))
        self.assertLess(message.index("steps.openvex.outcome == 'failure'"), message.index("steps.draft.outcome == 'failure'"))
        for want in ("Failed to generate or upload SBOM", "Failed to generate or upload OpenVEX", "actions/runs", "github.run_id"):
            self.assertIn(want, message)
        self.assertNotIn("steps.draft.outputs.message ||", message)

    def test_create_release_candidate_failure_uses_the_failed_step(self) -> None:
        message = self.job_steps("create-release-candidate")["Report Failure"]["with"]["message"]
        self.assertLess(message.index("steps.publish.outcome == 'failure'"), message.index("steps.draft.outcome == 'failure'"))
        self.assertLess(message.index("steps.draft.outcome == 'failure'"), message.index("steps.tag.outcome == 'failure'"))
        for step_id in ("publish", "draft", "tag"):
            self.assertIn(f"steps.{step_id}.outputs.message", message)
        self.assertIn("❌ Failed to create release candidate.", message)
        self.assertNotIn("steps.tag.outputs.message || steps.draft.outputs.message", message)
        for step_id, wording in (
            ("publish", "Failed to publish release candidate"),
            ("draft", "Failed to create draft release"),
            ("tag", "Failed to create and push tag"),
        ):
            with self.subTest(step=step_id):
                # A failed step's own message gets the run link appended.
                self.assertIn(f"(steps.{step_id}.outputs.message && format('{{0}} See the [failed workflow run]", message)
                # A failed step with no message still names itself and links the run.
                self.assertIn(f"steps.{step_id}.outputs.message, github.repository, github.run_id)) || format('❌ {wording}", message)
        self.assertEqual(message.count("actions/runs/"), 8)


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
