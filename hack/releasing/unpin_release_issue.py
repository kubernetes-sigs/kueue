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
import subprocess
import sys


ISSUE_QUERY = """
query($id: ID!) {
  node(id: $id) {
    ... on Issue { title closed isPinned }
  }
}
"""
UNPIN_MUTATION = """
mutation($id: ID!) {
  unpinIssue(input: {issueId: $id}) { issue { isPinned } }
}
"""


def graphql(query: str, issue_id: str) -> dict:
    result = subprocess.run(
        ["gh", "api", "graphql", "--input", "-"],
        input=json.dumps({"query": query, "variables": {"id": issue_id}}),
        capture_output=True,
        text=True,
        check=True,
    )
    response = json.loads(result.stdout)
    if response.get("errors"):
        raise ValueError(f"GitHub GraphQL errors: {response['errors']}")
    return response["data"]


def unpin_release_issue(issue_id: str) -> None:
    # Read the current state: the issue may have been reopened since the event.
    issue = graphql(ISSUE_QUERY, issue_id)["node"]
    if not issue:
        raise ValueError("Release issue was not found.")
    if not issue["title"].startswith(("Release v", "📦 Release v")):
        print("Skipping a non-release issue.")
        return
    if not issue["closed"] or not issue["isPinned"]:
        print("Skipping an open or already unpinned release issue.")
        return
    result = graphql(UNPIN_MUTATION, issue_id)
    if result["unpinIssue"]["issue"]["isPinned"]:
        raise ValueError("Release issue is still pinned after the mutation.")
    print("Unpinned the closed release issue.")


def main() -> int:
    parser = argparse.ArgumentParser(description="Unpin a closed release issue.")
    parser.add_argument("--issue-id", required=True)
    args = parser.parse_args()
    try:
        unpin_release_issue(args.issue_id)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f"Failed to unpin release issue: {error}", file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            print(error.stderr, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
