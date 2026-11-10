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

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


ISSUE_QUERY = """
query($id: ID!) {
  node(id: $id) {
    ... on Issue { title isPinned }
  }
}
"""


def parse_command(comment: str) -> bool:
    """Return the requested pin state from a standalone ChatOps command."""
    commands = {"/pin-release-issue": True, "/unpin-release-issue": False}
    command = comment.strip()
    if command not in commands:
        raise ValueError(
            "Use /pin-release-issue or /unpin-release-issue as a standalone comment."
        )
    return commands[command]


def graphql(query: str, issue_id: str) -> dict:
    """Execute a GraphQL operation and reject malformed or failed responses."""
    result = subprocess.run(
        ["gh", "api", "graphql", "--input", "-"],
        input=json.dumps({"query": query, "variables": {"id": issue_id}}),
        capture_output=True,
        text=True,
        check=True,
    )
    response = json.loads(result.stdout)
    if not isinstance(response, dict):
        raise ValueError("GitHub GraphQL response must be a mapping.")
    if response.get("errors"):
        raise ValueError(f"GitHub GraphQL errors: {response['errors']}")
    data = response.get("data")
    if not isinstance(data, dict):
        raise ValueError("GitHub GraphQL data must be a mapping.")
    return data


def set_release_issue_pin(issue_id: str, pinned: bool) -> bool:
    """Set the pin state and return whether this is a valid release issue."""
    issue = graphql(ISSUE_QUERY, issue_id)["node"]
    if not isinstance(issue, dict) or not isinstance(issue.get("title"), str):
        raise ValueError("Release issue was not found or has an invalid title.")
    if not issue["title"].startswith(("Release v", "📦 Release v")):
        print("Skipping a non-release issue.")
        return False
    if not isinstance(issue.get("isPinned"), bool):
        raise ValueError("Release issue has an invalid pin state.")
    if issue["isPinned"] == pinned:
        print("Release issue already has the requested pin state.")
        return True
    operation = "pinIssue" if pinned else "unpinIssue"
    mutation = f"""
mutation($id: ID!) {{
  {operation}(input: {{issueId: $id}}) {{ issue {{ isPinned }} }}
}}
"""
    result = graphql(mutation, issue_id)[operation]["issue"]
    if not isinstance(result, dict) or result.get("isPinned") is not pinned:
        raise ValueError("Release issue pin state did not match the request.")
    print("Pinned the release issue." if pinned else "Unpinned the release issue.")
    return True


def main() -> int:
    """Read a comment and apply its requested release-issue pin state."""
    parser = argparse.ArgumentParser(description="Pin or unpin a release issue.")
    parser.add_argument("--issue-id", required=True)
    parser.add_argument("--comment-file", required=True, type=Path)
    args = parser.parse_args()
    try:
        pinned = parse_command(args.comment_file.read_text(encoding="utf-8"))
        if set_release_issue_pin(args.issue_id, pinned) and os.environ.get("GITHUB_OUTPUT"):
            command = "pin-release-issue" if pinned else "unpin-release-issue"
            with Path(os.environ["GITHUB_OUTPUT"]).open("a", encoding="utf-8") as output:
                output.write(f"command={command}\n")
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f"Failed to update release issue pin: {error}", file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            print(error.stderr, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
