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

"""Enforces a maximum cap on open assigned issues per user."""

import json
import os
import sys
import urllib.parse
import urllib.request

DEFAULT_MAX_CAP = 8
API_BASE = "https://api.github.com"
REQUEST_TIMEOUT_SECONDS = 30


def github_request(method, url, token, payload=None):
    """Performs an authenticated GitHub REST API request."""
    headers = {
        "Accept": "application/vnd.github+json",
        "Authorization": f"Bearer {token}",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    data = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode("utf-8")

    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT_SECONDS) as resp:
        body = resp.read().decode("utf-8")
        return json.loads(body) if body else None


def list_open_assigned_issues(repo, assignee, token, request_fn=github_request):
    """Returns all open issues (excluding PRs) assigned to a user."""
    open_issues = []
    page = 1
    per_page = 100

    while True:
        query = urllib.parse.urlencode(
            {
                "state": "open",
                "assignee": assignee,
                "per_page": per_page,
                "page": page,
            }
        )
        url = f"{API_BASE}/repos/{repo}/issues?{query}"
        items = request_fn("GET", url, token)
        if not items:
            break

        open_issues.extend(item for item in items if "pull_request" not in item)

        if len(items) < per_page:
            break
        page += 1

    return open_issues


def enforce_cap(
    repo,
    assignee,
    issue_number,
    token,
    max_cap=DEFAULT_MAX_CAP,
    request_fn=github_request,
):
    """Unassigns the user and posts a comment if they exceed max_cap open issues."""
    if not assignee:
        print("No assignee found in event payload.")
        return

    open_issues = list_open_assigned_issues(repo, assignee, token, request_fn=request_fn)
    open_count = len(open_issues)
    print(f"User @{assignee} currently has {open_count} open assigned issues (cap: {max_cap}).")

    if open_count <= max_cap:
        return

    print(f"Cap exceeded ({open_count} > {max_cap}). Unassigning @{assignee} from issue #{issue_number}.")

    unassign_url = f"{API_BASE}/repos/{repo}/issues/{issue_number}/assignees"
    request_fn("DELETE", unassign_url, token, payload={"assignees": [assignee]})

    other_count = open_count - 1
    guide_url = f"https://github.com/{repo}/blob/main/CONTRIBUTING.md#issue-assignment-policy"
    comment_body = (
        f"@{assignee}: Users are limited to a maximum of **{max_cap}** open assigned issues at a time "
        f"(you currently have **{other_count}** other open issues assigned).\n\n"
        f"You have been automatically unassigned from this issue so others can work on it. "
        f"Please see our [contributor guide]({guide_url}) and complete or `/unassign` one of your "
        f"existing issues before assigning a new one."
    )

    comment_url = f"{API_BASE}/repos/{repo}/issues/{issue_number}/comments"
    request_fn("POST", comment_url, token, payload={"body": comment_body})


def main():
    token = os.environ.get("GITHUB_TOKEN", "")
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    assignee = os.environ.get("ASSIGNEE", "")
    issue_number = os.environ.get("ISSUE_NUMBER", "")
    max_cap = int(os.environ.get("MAX_CAP", str(DEFAULT_MAX_CAP)))

    if not token or not repo or not issue_number:
        print("Missing required environment variables: GITHUB_TOKEN, GITHUB_REPOSITORY, ISSUE_NUMBER", file=sys.stderr)
        sys.exit(1)

    enforce_cap(
        repo=repo,
        assignee=assignee,
        issue_number=int(issue_number),
        token=token,
        max_cap=max_cap,
    )


if __name__ == "__main__":
    main()
