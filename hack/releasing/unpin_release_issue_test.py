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
import subprocess
import unittest
from unittest import mock

import unpin_release_issue


class UnpinReleaseIssueTest(unittest.TestCase):
    def test_command(self) -> None:
        issue = {"title": "📦 Release v0.18.0", "closed": True, "isPinned": True}
        cases = [
            ("closed pinned release", issue, False, 0, True),
            ("legacy title", {**issue, "title": "Release v0.18.0"}, False, 0, True),
            ("ordinary issue", {**issue, "title": "Fix release automation"}, False, 0, False),
            ("reopened release", {**issue, "closed": False}, False, 0, False),
            ("already unpinned", {**issue, "isPinned": False}, False, 0, False),
            ("missing issue", None, False, 1, False),
            ("wrong node type", {}, False, 1, False),
            ("mutation did not unpin", issue, True, 1, True),
        ]
        for name, current_issue, pinned_after, expected_exit, expect_mutation in cases:
            with self.subTest(name=name):
                responses = [subprocess.CompletedProcess(
                    [], 0, json.dumps({"data": {"node": current_issue}}), ""
                )]
                if expect_mutation:
                    responses.append(subprocess.CompletedProcess(
                        [], 0, json.dumps({"data": {"unpinIssue": {"issue": {"isPinned": pinned_after}}}}), ""
                    ))
                stdout, stderr = io.StringIO(), io.StringIO()
                with mock.patch.object(subprocess, "run", side_effect=responses) as run, \
                     mock.patch("sys.argv", ["unpin_release_issue.py", "--issue-id", "I_release"]), \
                     contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                    self.assertEqual(expected_exit, unpin_release_issue.main())
                self.assertEqual(2 if expect_mutation else 1, run.call_count)
                for call in run.call_args_list:
                    self.assertEqual(["gh", "api", "graphql", "--input", "-"], call.args[0])
                    self.assertEqual({"id": "I_release"}, json.loads(call.kwargs["input"])["variables"])
                    self.assertTrue(call.kwargs["check"])
                query = json.loads(run.call_args_list[0].kwargs["input"])["query"]
                self.assertIn("node(id: $id)", query)
                for field in ("title", "closed", "isPinned"):
                    self.assertIn(field, query)
                if expect_mutation:
                    mutation = json.loads(run.call_args_list[1].kwargs["input"])["query"]
                    self.assertIn("unpinIssue(input: {issueId: $id})", mutation)
                if expected_exit:
                    self.assertTrue(stderr.getvalue())
                    self.assertNotIn("Unpinned", stdout.getvalue())
                else:
                    self.assertEqual("", stderr.getvalue())
                    self.assertIn("Unpinned" if expect_mutation else "Skipping", stdout.getvalue())

    def test_api_errors_fail_the_command(self) -> None:
        cases = [
            ("CLI failure", subprocess.CalledProcessError(1, ["gh"], stderr="permission denied")),
            ("gh unavailable", FileNotFoundError("gh")),
            ("GraphQL errors", '{"errors": [{"message": "permission denied"}], "data": null}'),
            ("invalid JSON", "not JSON"),
            ("missing data", "{}"),
            ("null data", '{"data": null}'),
        ]
        issue_response = subprocess.CompletedProcess([], 0, json.dumps({"data": {"node": {
            "title": "📦 Release v0.18.0", "closed": True, "isPinned": True,
        }}}), "")
        for name, failure in cases:
            for phase in ("query", "mutation"):
                with self.subTest(name=name, phase=phase):
                    response = failure if isinstance(failure, Exception) else subprocess.CompletedProcess([], 0, failure, "")
                    responses = [response] if phase == "query" else [issue_response, response]
                    stdout, stderr = io.StringIO(), io.StringIO()
                    with mock.patch.object(subprocess, "run", side_effect=responses) as run, \
                         mock.patch("sys.argv", ["unpin_release_issue.py", "--issue-id", "I_release"]), \
                         contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                        self.assertEqual(1, unpin_release_issue.main())
                    self.assertEqual(len(responses), run.call_count)
                    self.assertTrue(stderr.getvalue())
                    self.assertEqual("", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
