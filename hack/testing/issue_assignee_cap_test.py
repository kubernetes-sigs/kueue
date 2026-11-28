#!/usr/bin/env python3

# Copyright The Kubernetes Authors.
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

"""Unit tests for hack/issue_assignee_cap.py."""

import os
import sys
import tempfile
import unittest
import urllib.parse

# Ensure hack/ is importable when running from repo root
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

import issue_assignee_cap  # noqa: E402


class TestIssueAssigneeCap(unittest.TestCase):
    def test_missing_assignee_exits_early(self):
        calls = []

        def fake_request(method, url, token, payload=None):
            calls.append((method, url, payload))
            return []

        issue_assignee_cap.enforce_cap(
            repo="kubernetes-sigs/kueue",
            assignee="",
            issue_number=123,
            token="test-token",
            request_fn=fake_request,
        )
        self.assertEqual(calls, [])

    def test_at_or_below_cap_does_not_unassign(self):
        calls = []

        def fake_request(method, url, token, payload=None):
            calls.append((method, url, payload))
            return [{"number": i} for i in range(1, 9)]

        issue_assignee_cap.enforce_cap(
            repo="kubernetes-sigs/kueue",
            assignee="test-user",
            issue_number=123,
            token="test-token",
            max_cap=8,
            request_fn=fake_request,
            exempt_users=set(),
        )

        self.assertEqual(len(calls), 1)
        method, url, _ = calls[0]
        self.assertEqual(method, "GET")
        params = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
        self.assertEqual(params["state"], ["open"])
        self.assertEqual(params["assignee"], ["test-user"])
        self.assertEqual(params["per_page"], ["100"])
        self.assertEqual(params["page"], ["1"])

    def test_over_cap_unassigns_and_comments(self):
        calls = []

        def fake_request(method, url, token, payload=None):
            calls.append((method, url, payload))
            if method == "GET":
                return [{"number": i} for i in range(1, 10)]
            return {}

        issue_assignee_cap.enforce_cap(
            repo="kubernetes-sigs/kueue",
            assignee="test-user",
            issue_number=42,
            token="test-token",
            max_cap=8,
            request_fn=fake_request,
            exempt_users=set(),
        )

        self.assertEqual(len(calls), 3)

        # 1. GET open assigned issues
        self.assertEqual(calls[0][0], "GET")
        params = urllib.parse.parse_qs(urllib.parse.urlparse(calls[0][1]).query)
        self.assertEqual(params["state"], ["open"])
        self.assertEqual(params["assignee"], ["test-user"])

        # 2. DELETE assignee
        self.assertEqual(calls[1][0], "DELETE")
        self.assertTrue(calls[1][1].endswith("/repos/kubernetes-sigs/kueue/issues/42/assignees"))
        self.assertEqual(calls[1][2], {"assignees": ["test-user"]})

        # 3. POST comment
        self.assertEqual(calls[2][0], "POST")
        self.assertTrue(calls[2][1].endswith("/repos/kubernetes-sigs/kueue/issues/42/comments"))
        body = calls[2][2]["body"]
        self.assertIn("maximum of **8** open assigned issues", body)
        self.assertIn("currently have **8** other open issues", body)
        self.assertIn("CONTRIBUTING.md#issue-assignment-policy", body)

    def test_excludes_pull_requests(self):
        calls = []

        def fake_request(method, url, token, payload=None):
            calls.append((method, url, payload))
            # 9 total items, 1 is a PR -> 8 actual issues (within cap)
            items = [{"number": i} for i in range(1, 9)]
            items.append({"number": 9, "pull_request": {"url": "https://api.github.com/..."}})
            return items

        issue_assignee_cap.enforce_cap(
            repo="kubernetes-sigs/kueue",
            assignee="test-user",
            issue_number=123,
            token="test-token",
            max_cap=8,
            request_fn=fake_request,
            exempt_users=set(),
        )

        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][0], "GET")

    def test_pagination_across_multiple_pages(self):
        calls = []

        def fake_request(method, url, token, payload=None):
            calls.append((method, url, payload))
            if method == "GET":
                params = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
                if params["page"] == ["1"]:
                    return [{"number": i} for i in range(1, 101)]
                return [{"number": 101}]
            return {}

        issue_assignee_cap.enforce_cap(
            repo="kubernetes-sigs/kueue",
            assignee="test-user",
            issue_number=101,
            token="test-token",
            max_cap=8,
            request_fn=fake_request,
            exempt_users=set(),
        )

        # 2 GET pages + 1 DELETE + 1 POST
        self.assertEqual(len(calls), 4)
        self.assertEqual(calls[0][0], "GET")
        first_params = urllib.parse.parse_qs(urllib.parse.urlparse(calls[0][1]).query)
        self.assertEqual(first_params["page"], ["1"])
        self.assertEqual(calls[1][0], "GET")
        second_params = urllib.parse.parse_qs(urllib.parse.urlparse(calls[1][1]).query)
        self.assertEqual(second_params["page"], ["2"])
        self.assertEqual(calls[2][0], "DELETE")
        self.assertEqual(calls[3][0], "POST")

    def test_load_top_level_approvers_resolves_aliases_and_excludes_reviewers_and_emeritus(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            with open(os.path.join(tmpdir, "OWNERS_ALIASES"), "w", encoding="utf-8") as f:
                f.write(
                    "aliases:\n"
                    "  kueue-approvers:\n"
                    "- mimowo\n"
                    "- sohankunkerkar\n"
                    "- tenzen-y # lead\n"
                    "  kueue-reviewers:\n"
                    "- mbobrovskyi\n"
                    "  test-approvers:\n"
                    "- olekzabl\n"
                )
            with open(os.path.join(tmpdir, "OWNERS"), "w", encoding="utf-8") as f:
                f.write(
                    "filters:\n"
                    '  ".*":\n'
                    "    approvers:\n"
                    "- kueue-approvers\n"
                    "    reviewers:\n"
                    "- kueue-reviewers\n"
                    "    emeritus_approvers:\n"
                    "- ahg-g\n"
                    '  "_test\\\\.go$":\n'
                    "    approvers:\n"
                    "- test-approvers\n"
                )

            approvers = issue_assignee_cap.load_top_level_approvers(repo_root=tmpdir)
            self.assertEqual(approvers, {"mimowo", "sohankunkerkar", "tenzen-y"})
            self.assertNotIn("mbobrovskyi", approvers)
            self.assertNotIn("ahg-g", approvers)
            self.assertNotIn("olekzabl", approvers)

    def test_exempt_top_level_approver_is_not_unassigned_when_over_cap(self):
        calls = []

        def fake_request(method, url, token, payload=None):
            calls.append((method, url, payload))
            return [{"number": i} for i in range(1, 12)]

        issue_assignee_cap.enforce_cap(
            repo="kubernetes-sigs/kueue",
            assignee="SohanKunkerkar",
            issue_number=19547,
            token="test-token",
            max_cap=8,
            request_fn=fake_request,
            exempt_users={"mimowo", "sohankunkerkar", "tenzen-y"},
        )
        self.assertEqual(calls, [])

    def test_non_exempt_contributor_is_unassigned_when_over_cap(self):
        calls = []

        def fake_request(method, url, token, payload=None):
            calls.append((method, url, payload))
            if method == "GET":
                return [{"number": i} for i in range(1, 10)]
            return {}

        issue_assignee_cap.enforce_cap(
            repo="kubernetes-sigs/kueue",
            assignee="contributor-user",
            issue_number=19547,
            token="test-token",
            max_cap=8,
            request_fn=fake_request,
            exempt_users={"mimowo", "sohankunkerkar", "tenzen-y"},
        )
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls[1][0], "DELETE")
        self.assertEqual(calls[2][0], "POST")

if __name__ == "__main__":
    unittest.main()
