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

import release_issue_pin


class ReleaseIssuePinTest(unittest.TestCase):
    def run_command(self, command, responses):
        """Run the CLI with mocked GitHub responses."""
        argv = ["release_issue_pin.py", "--issue-id", "I_release", "--command", command]
        stdout, stderr = io.StringIO(), io.StringIO()
        with mock.patch.object(subprocess, "run", side_effect=responses) as run, \
             mock.patch.object(sys, "argv", argv), \
             contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            status = release_issue_pin.main()
        return status, stdout.getvalue(), stderr.getvalue(), run.call_args_list

    def test_commands(self):
        """Support both commands on open or closed releases and make retries safe."""
        cases = {
            "pin open release": ("pin-release-issue", "📦 Release v0.18.0", False, False),
            "pin closed release": ("pin-release-issue", "📦 Release v0.18.0", True, False),
            "restore pin after reopening": ("pin-release-issue", "Release v0.18.0", False, False),
            "unpin open release": ("unpin-release-issue", "📦 Release v0.18.0", False, True),
            "unpin closed release": ("unpin-release-issue", "Release v0.18.0", True, True),
            "pin already pinned": ("pin-release-issue", "Release v0.18.0", False, True),
            "unpin already unpinned": ("unpin-release-issue", "Release v0.18.0", True, False),
        }
        for name, (command, title, closed, initial_pin) in cases.items():
            with self.subTest(name=name):
                desired = command == "pin-release-issue"
                mutate = desired != initial_pin
                operation = "pinIssue" if desired else "unpinIssue"
                responses = [subprocess.CompletedProcess([], 0, json.dumps({"data": {"node": {"title": title, "closed": closed, "isPinned": initial_pin}}}), "")]
                if mutate:
                    responses.append(subprocess.CompletedProcess([], 0, json.dumps({"data": {operation: {"issue": {"isPinned": desired}}}}), ""))
                status, stdout, stderr, calls = self.run_command(command, responses)
                self.assertEqual(0, status, stderr)
                self.assertEqual("", stderr)
                self.assertEqual(2 if mutate else 1, len(calls))
                self.assertTrue(stdout)
                for call in calls:
                    self.assertEqual(["gh", "api", "graphql", "--input", "-"], call.args[0])
                    self.assertEqual({"id": "I_release"}, json.loads(call.kwargs["input"])["variables"])
                if mutate:
                    self.assertIn(operation + "(input: {issueId: $id})", json.loads(calls[1].kwargs["input"])["query"])

    def test_renamed_issue_is_not_modified(self):
        """An issue may have been renamed after the workflow parsed the event."""
        for title in ["Fix release automation", "Release v0.18.0 planning", "Release vNext"]:
            with self.subTest(title=title):
                response = {"data": {"node": {"title": title, "isPinned": True}}}
                query = subprocess.CompletedProcess([], 0, json.dumps(response), "")
                status, stdout, stderr, calls = self.run_command("unpin-release-issue", [query])
                self.assertEqual(1, status)
                self.assertEqual(1, len(calls))
                self.assertEqual("", stdout)
                self.assertIn("no longer has a release title", stderr)

    def test_api_failures(self):
        """Fail cleanly on query/mutation errors or malformed JSON envelopes."""
        query_response = subprocess.CompletedProcess([], 0, json.dumps({"data": {"node": {"title": "Release v0.18.0", "isPinned": True}}}), "")
        failures = [subprocess.CalledProcessError(1, ["gh"], stderr="permission denied"), FileNotFoundError("gh"), "not JSON", "null", "[]", '"text"', "{}", '{"data": null}', '{"data": []}', '{"data": "text"}', '{"errors": [{"message": "permission denied"}], "data": null}']
        for failure in failures:
            for phase in ("query", "mutation"):
                with self.subTest(failure=failure, phase=phase):
                    response = failure if isinstance(failure, Exception) else subprocess.CompletedProcess([], 0, failure, "")
                    responses = [response] if phase == "query" else [query_response, response]
                    status, stdout, stderr, calls = self.run_command("unpin-release-issue", responses)
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
                status, stdout, stderr, calls = self.run_command("pin-release-issue", [result])
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
                    status, stdout, stderr, _ = self.run_command("pin-release-issue" if pinned else "unpin-release-issue", [query, mutation])
                    self.assertEqual(1, status)
                    self.assertEqual("", stdout)
                    self.assertNotIn("Traceback", stderr)


class ReleaseIssuePinWorkflowTest(unittest.TestCase):
    def setUp(self):
        root = Path(__file__).resolve().parents[2]
        path = root / ".github/workflows/release-utils.yml"
        self.workflow = yaml.load(path.read_text(), Loader=yaml.BaseLoader)
        self.authorize = self.workflow["jobs"]["authorize"]

    def test_authorization_and_event_wiring(self):
        """Use the existing per-issue workflow and wait for authorization."""
        self.assertEqual({"issue_comment": {"types": ["created"]}}, self.workflow["on"])
        self.assertIn("github.event.issue.pull_request == null", self.authorize["if"])
        for command in ("pin-release-issue", "unpin-release-issue"):
            self.assertIn(f"contains(github.event.comment.body, '/{command}')", self.authorize["if"])

        steps = {step["id"]: step for step in self.authorize["steps"] if "id" in step}
        auth = steps["auth"]
        self.assertEqual("$/.github/actions/authorize-release-actor", auth["uses"])
        self.assertEqual(["release-team", "kueue-approvers"], auth["with"]["aliases"].split())
        self.assertLess(list(steps).index("auth"), list(steps).index("parse"))
        self.assertEqual(
            "${{ steps.parse.outputs.command != 'pin-release-issue' && "
            "steps.parse.outputs.command != 'unpin-release-issue' }}",
            steps["check_published"]["if"],
        )

        job = self.workflow["jobs"]["release-issue-pin"]
        self.assertEqual("authorize", job["needs"])
        self.assertEqual(
            "${{ needs.authorize.outputs.command == 'pin-release-issue' || "
            "needs.authorize.outputs.command == 'unpin-release-issue' }}",
            job["if"],
        )
        self.assertEqual({"contents": "read", "issues": "write"}, job["permissions"])
        checkout, update, success, failure = job["steps"]
        self.assertEqual("false", checkout["with"]["persist-credentials"])
        self.assertEqual("${{ needs.authorize.outputs.command }}", update["env"]["COMMAND"])
        self.assertEqual("${{ github.event.issue.node_id }}", update["env"]["ISSUE_ID"])
        self.assertIn('--issue-id "$ISSUE_ID" --command "$COMMAND"', update["run"])
        self.assertEqual("${{ success() }}", success["if"])
        self.assertEqual("$/.github/actions/report-result", success["uses"])
        self.assertEqual("${{ needs.authorize.outputs.command }}", success["with"]["command"])
        self.assertEqual("${{ needs.authorize.outputs.command == 'unpin-release-issue' }}", success["with"]["mark-done"])
        self.assertEqual("${{ failure() }}", failure["if"])
        self.assertEqual("$/.github/actions/report-result", failure["uses"])
        self.assertNotIn("mark-done", failure["with"])

    def test_shared_parser_accepts_only_standalone_pin_commands(self):
        """Exercise the same shell parser that supplies the job's command."""
        parse = next(step for step in self.authorize["steps"] if step.get("id") == "parse")
        cases = [
            ("/pin-release-issue", "pin-release-issue"),
            (" /unpin-release-issue\r\n", "unpin-release-issue"),
            ("/publish-release", "publish-release"),
            ("", None),
            ("please /pin-release-issue", None),
            ("/pin-release-issues", None),
            ("/pin-release-issue 123", None),
            ("/pin-release-issue\n/unpin-release-issue", None),
            ("```\n/pin-release-issue\n```", None),
            ("> /pin-release-issue", None),
            ("/pin-release-issue; echo injected", None),
            ("/pin-release-issue\nThanks", None),
        ]
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            for body, command in cases:
                with self.subTest(body=body):
                    output.write_text("")
                    result = subprocess.run(
                        ["bash", "-e", "-c", parse["run"]],
                        env={**os.environ, "BODY": body, "TITLE": "📦 Release v0.18.0",
                             "COMMENT": "https://example.com/comment", "ISSUE_BODY": "",
                             "GITHUB_OUTPUT": str(output)},
                        text=True, capture_output=True,
                    )
                    lines = output.read_text().splitlines()
                    if command is None:
                        self.assertEqual(1, result.returncode, result.stderr)
                        self.assertFalse(any(line.startswith("command=") for line in lines))
                    else:
                        self.assertEqual(0, result.returncode, result.stderr)
                        self.assertIn(f"command={command}", lines)

    def test_release_checklist(self):
        """Remind releasers to unpin before closing the issue."""
        root = Path(__file__).resolve().parents[2]
        template = (root / ".github/ISSUE_TEMPLATE/NEW_RELEASE.md").read_text()
        self.assertIn("Run the ChatOps command `/unpin-release-issue` on this issue, then close it. <!-- step:unpin-release-issue -->", template)


if __name__ == "__main__":
    unittest.main()
