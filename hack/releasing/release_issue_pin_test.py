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
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import yaml

import authorize_release_actor
import release_issue_pin


class ReleaseIssuePinTest(unittest.TestCase):
    def run_command(self, comment, responses):
        """Run the real CLI with a comment file and mocked GitHub responses."""
        with tempfile.TemporaryDirectory() as directory:
            comment_file = Path(directory) / "comment.md"
            comment_file.write_text(comment, encoding="utf-8")
            output_file = Path(directory) / "output"
            stdout, stderr = io.StringIO(), io.StringIO()
            with mock.patch.dict(os.environ, {"GITHUB_OUTPUT": str(output_file)}), \
                 mock.patch.object(subprocess, "run", side_effect=responses) as run, \
                 mock.patch.object(sys, "argv", ["release_issue_pin.py", "--issue-id", "I_release", "--comment-file", str(comment_file)]), \
                 contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                status = release_issue_pin.main()
            return status, stdout.getvalue(), stderr.getvalue(), run.call_args_list, output_file.read_text() if output_file.exists() else ""

    def test_commands(self):
        """Support both commands on open or closed releases and make retries safe."""
        cases = {
            "pin open release": ("/pin-release-issue", "📦 Release v0.18.0", False, False),
            "pin closed release": ("/pin-release-issue", "📦 Release v0.18.0", True, False),
            "restore pin after reopening": ("/pin-release-issue", "Release v0.18.0", False, False),
            "unpin open release": ("/unpin-release-issue", "📦 Release v0.18.0", False, True),
            "unpin closed release": ("/unpin-release-issue", "Release v0.18.0", True, True),
            "trailing newline": ("/unpin-release-issue\r\n", "Release v0.18.0", False, True),
            "pin already pinned": ("/pin-release-issue", "Release v0.18.0", False, True),
            "unpin already unpinned": ("/unpin-release-issue", "Release v0.18.0", True, False),
            "ordinary issue": ("/pin-release-issue", "Fix release automation", False, False),
        }
        for name, (comment, title, closed, initial_pin) in cases.items():
            with self.subTest(name=name):
                desired = comment.strip() == "/pin-release-issue"
                mutate = title.startswith(("Release v", "📦 Release v")) and desired != initial_pin
                operation = "pinIssue" if desired else "unpinIssue"
                responses = [subprocess.CompletedProcess([], 0, json.dumps({"data": {"node": {"title": title, "closed": closed, "isPinned": initial_pin}}}), "")]
                if mutate:
                    responses.append(subprocess.CompletedProcess([], 0, json.dumps({"data": {operation: {"issue": {"isPinned": desired}}}}), ""))
                status, stdout, stderr, calls, output = self.run_command(comment, responses)
                self.assertEqual(0, status, stderr)
                self.assertEqual("", stderr)
                self.assertEqual(2 if mutate else 1, len(calls))
                self.assertTrue(stdout)
                self.assertEqual(f"command={comment.strip()[1:]}\n" if title.startswith(("Release v", "📦 Release v")) else "", output)
                for call in calls:
                    self.assertEqual(["gh", "api", "graphql", "--input", "-"], call.args[0])
                    self.assertEqual({"id": "I_release"}, json.loads(call.kwargs["input"])["variables"])
                if mutate:
                    self.assertIn(operation + "(input: {issueId: $id})", json.loads(calls[1].kwargs["input"])["query"])

    def test_invalid_comments_never_call_github(self):
        """Reject quotes, examples, arguments, conflicting commands and shell text."""
        for comment in ["", "please /pin-release-issue", "/pin-release-issues", "/pin-release-issue 123", "/pin-release-issue\n/unpin-release-issue", "```\n/pin-release-issue\n```", "> /pin-release-issue", "/pin-release-issue; echo injected", "/pin-release-issue\nThanks"]:
            with self.subTest(comment=comment):
                status, stdout, stderr, calls, output = self.run_command(comment, [])
                self.assertEqual(1, status)
                self.assertEqual([], calls)
                self.assertEqual("", stdout)
                self.assertEqual("", output)
                self.assertIn("standalone comment", stderr)

    def test_api_failures(self):
        """Fail cleanly on query/mutation errors or malformed JSON envelopes."""
        query_response = subprocess.CompletedProcess([], 0, json.dumps({"data": {"node": {"title": "Release v0.18.0", "isPinned": True}}}), "")
        failures = [subprocess.CalledProcessError(1, ["gh"], stderr="permission denied"), FileNotFoundError("gh"), "not JSON", "null", "[]", '"text"', "{}", '{"data": null}', '{"data": []}', '{"data": "text"}', '{"errors": [{"message": "permission denied"}], "data": null}']
        for failure in failures:
            for phase in ("query", "mutation"):
                with self.subTest(failure=failure, phase=phase):
                    response = failure if isinstance(failure, Exception) else subprocess.CompletedProcess([], 0, failure, "")
                    responses = [response] if phase == "query" else [query_response, response]
                    status, stdout, stderr, calls, output = self.run_command("/unpin-release-issue", responses)
                    self.assertEqual(1, status)
                    self.assertEqual("", stdout)
                    self.assertTrue(stderr)
                    self.assertNotIn("Traceback", stderr)
                    self.assertEqual(len(responses), len(calls))

    def test_invalid_issue_and_mutation_states(self):
        """Reject missing/wrong node types and unconfirmed mutation results."""
        for node in [None, {}, [], "text", {"title": None}, {"title": "Release v0.18.0", "isPinned": None}, {"title": "Release v0.18.0", "isPinned": 1}]:
            with self.subTest(node=node):
                result = subprocess.CompletedProcess([], 0, json.dumps({"data": {"node": node}}), "")
                status, stdout, stderr, calls, output = self.run_command("/pin-release-issue", [result])
                self.assertEqual(1, status)
                self.assertEqual("", stdout)
                self.assertNotIn("Traceback", stderr)
                self.assertEqual(1, len(calls))
        for pinned in (True, False):
            operation = "pinIssue" if pinned else "unpinIssue"
            query = subprocess.CompletedProcess([], 0, json.dumps({"data": {"node": {"title": "Release v0.18.0", "isPinned": not pinned}}}), "")
            for result in [None, [], "text", {}, {"isPinned": not pinned}, {"isPinned": int(pinned)}]:
                with self.subTest(pinned=pinned, result=result):
                    mutation = subprocess.CompletedProcess([], 0, json.dumps({"data": {operation: {"issue": result}}}), "")
                    status, stdout, stderr, _, output = self.run_command("/pin-release-issue" if pinned else "/unpin-release-issue", [query, mutation])
                    self.assertEqual(1, status)
                    self.assertEqual("", stdout)
                    self.assertNotIn("Traceback", stderr)


class ReleaseIssuePinWorkflowTest(unittest.TestCase):
    def test_authorization_and_event_wiring(self):
        """Keep writes behind shared alias authorization and comment-only events."""
        root = Path(__file__).resolve().parents[2]
        workflow = yaml.load((root / ".github/workflows/release-issue-pinning.yml").read_text(), Loader=yaml.BaseLoader)
        self.assertEqual({"issue_comment": {"types": ["created"]}}, workflow["on"])
        job = workflow["jobs"]["pin"]
        self.assertNotIn("concurrency", workflow)
        self.assertEqual("release-issue-${{ github.event.issue.number }}", job["concurrency"]["group"])
        release_workflow = yaml.load((root / ".github/workflows/release-utils.yml").read_text(), Loader=yaml.BaseLoader)
        self.assertEqual(job["concurrency"], release_workflow["concurrency"])
        self.assertEqual("max", job["concurrency"]["queue"])
        self.assertIn("github.event.issue.pull_request == null", job["if"])
        steps = job["steps"]
        auth = next(step for step in steps if step.get("uses") == "./.github/actions/authorize-release-actor")
        aliases = auth["with"]["aliases"].split()
        self.assertEqual(["release-team", "kueue-approvers"], aliases)
        self.assertEqual("${{ github.actor }}", auth["with"]["actor"])
        update = next(step for step in steps if "release_issue_pin.py" in step.get("run", ""))
        self.assertLess(steps.index(auth), steps.index(update))
        self.assertNotIn("if", update)  # GitHub's default success() prevents writes after denied auth.
        checkout = steps[0]["with"]
        self.assertEqual("false", checkout["persist-credentials"])
        self.assertEqual("false", checkout["sparse-checkout-cone-mode"])
        self.assertIn("OWNERS_ALIASES", checkout["sparse-checkout"])
        report = steps[-1]
        self.assertEqual("./.github/actions/report-result", report["uses"])
        self.assertEqual("unpin-release-issue", report["with"]["command"])
        self.assertEqual("true", report["with"]["mark-done"])
        self.assertEqual("steps.pin.outputs.command == 'unpin-release-issue'", report["if"])
        self.assertEqual("write", job["permissions"]["issues"])
        owners = "aliases:\n  release-team: [release-user]\n  kueue-approvers: [approver]\n"
        for actor, allowed in [("release-user", True), ("approver", True), ("untrusted", False)]:
            with self.subTest(actor=actor):
                self.assertEqual(allowed, authorize_release_actor.is_authorized(actor, owners, aliases))

    def test_release_checklist(self):
        """Remind releasers to unpin before closing the issue."""
        root = Path(__file__).resolve().parents[2]
        template = (root / ".github/ISSUE_TEMPLATE/NEW_RELEASE.md").read_text()
        self.assertIn("Run the ChatOps command `/unpin-release-issue` on this issue, then close it. <!-- step:unpin-release-issue -->", template)


if __name__ == "__main__":
    unittest.main()
