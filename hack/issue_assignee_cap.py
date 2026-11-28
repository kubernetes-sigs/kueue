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
import re
import sys
import urllib.parse
import urllib.request

DEFAULT_MAX_CAP = 8
API_BASE = "https://api.github.com"
REQUEST_TIMEOUT_SECONDS = 30
REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

def _strip_comment(line):
    return line.split("#", 1)[0].rstrip()


def _parse_owners_aliases(aliases_path):
    """Parses OWNERS_ALIASES into a lowercase {alias: set(users)} mapping."""
    aliases = {}
    if not os.path.isfile(aliases_path):
        return aliases

    in_aliases = False
    current_alias = None
    with open(aliases_path, encoding="utf-8") as f:
        for raw_line in f:
            line = _strip_comment(raw_line)
            if not line.strip():
                continue

            if re.match(r"^aliases:\s*$", line):
                in_aliases = True
                current_alias = None
                continue

            if not line[0].isspace() and not line.startswith("-"):
                in_aliases = False
                current_alias = None
                continue

            if not in_aliases:
                continue

            alias_match = re.match(r"^\s+([A-Za-z0-9_-]+):\s*$", line)
            if alias_match:
                current_alias = alias_match.group(1).lower()
                aliases.setdefault(current_alias, set())
                continue

            item_match = re.match(r"^\s*-\s+([A-Za-z0-9_-]+)\s*$", line)
            if item_match and current_alias:
                aliases[current_alias].add(item_match.group(1).lower())

    return aliases


def load_top_level_approvers(repo_root=REPO_ROOT):
    """Returns lowercase usernames of active top-level approvers from root OWNERS."""
    owners_path = os.path.join(repo_root, "OWNERS")
    aliases_path = os.path.join(repo_root, "OWNERS_ALIASES")

    if not os.path.isfile(owners_path):
        return set()

    aliases = _parse_owners_aliases(aliases_path)
    approvers = set()
    in_filters = False
    filter_indent = None
    in_catchall = False
    in_approvers = False

    with open(owners_path, encoding="utf-8") as f:
        for raw_line in f:
            line = _strip_comment(raw_line)
            if not line.strip():
                continue

            item_match = re.match(r"^\s*-\s+([A-Za-z0-9_-]+)\s*$", line)
            if item_match:
                if in_approvers:
                    entry = item_match.group(1).lower()
                    if entry in aliases:
                        approvers.update(aliases[entry])
                    else:
                        approvers.add(entry)
                continue

            indent = len(line) - len(line.lstrip(" "))
            stripped = line.strip()

            if indent == 0:
                in_approvers = False
                in_catchall = False
                filter_indent = None
                if stripped == "filters:":
                    in_filters = True
                elif stripped == "approvers:":
                    in_filters = False
                    in_approvers = True
                else:
                    in_filters = False
                continue

            if in_filters:
                if filter_indent is None or indent <= filter_indent:
                    filter_indent = indent
                    in_approvers = False
                    key = stripped[:-1].strip().strip("\"'") if stripped.endswith(":") else ""
                    in_catchall = key == ".*"
                elif in_catchall:
                    in_approvers = stripped == "approvers:"

    return approvers

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
    exempt_users=None,
):
    """Unassigns the user and posts a comment if they exceed max_cap open issues."""
    if not assignee:
        print("No assignee found in event payload.")
        return

    if exempt_users is None:
        exempt_users = load_top_level_approvers()
    else:
        exempt_users = {u.lower() for u in exempt_users}

    if assignee.lower() in exempt_users:
        print(f"User @{assignee} is a top-level approver in OWNERS. Exempt from issue cap.")
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
