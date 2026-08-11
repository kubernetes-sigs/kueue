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
import re
from pathlib import Path


COMMAND = "/set-release-note"
MAX_PR_BODY_LENGTH = 65_536
RELEASE_NOTE_START_RE = re.compile(r"(?m)^[ \t]*```release-note[ \t]*\r?$")
RELEASE_NOTE_BLOCK_RE = re.compile(
    r"(?ms)^[ \t]*```release-note[ \t]*\r?\n.*?^[ \t]*```[ \t]*\r?$"
)


def parse_release_note(comment: str) -> str:
    """Parse and validate the release note from a ChatOps comment."""
    lines = comment.replace("\r\n", "\n").strip().splitlines()
    if not lines or lines[0].strip() != COMMAND:
        raise ValueError(f"{COMMAND} must be the first line of the comment.")

    release_note = "\n".join(lines[1:]).strip()
    if not release_note:
        raise ValueError(f"{COMMAND} must be followed by a non-empty release note.")
    if "```" in release_note:
        raise ValueError("The release note must not contain a fenced code block.")
    return release_note


def set_release_note(pr_body: str, release_note: str) -> str:
    """Replace an existing release-note block or append a standard block."""
    starts = list(RELEASE_NOTE_START_RE.finditer(pr_body))
    blocks = list(RELEASE_NOTE_BLOCK_RE.finditer(pr_body))
    if len(starts) > 1:
        raise ValueError("The pull request body contains multiple release-note blocks.")

    new_block = f"```release-note\n{release_note}\n```"
    if blocks:
        block = blocks[0]
        updated_body = f"{pr_body[:block.start()]}{new_block}{pr_body[block.end():]}"
    elif starts:
        updated_body = f"{pr_body[:starts[0].start()]}{new_block}\n"
    elif pr_body.strip():
        updated_body = f"{pr_body.rstrip()}\n\n{new_block}\n"
    else:
        updated_body = f"{new_block}\n"

    if len(updated_body) > MAX_PR_BODY_LENGTH:
        raise ValueError(
            f"The updated pull request body exceeds {MAX_PR_BODY_LENGTH} characters."
        )
    return updated_body


def main() -> int:
    """Read file arguments and generate the updated pull request body."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--comment-file", required=True, type=Path)
    parser.add_argument("--body-file", required=True, type=Path)
    parser.add_argument("--output-file", required=True, type=Path)
    args = parser.parse_args()

    try:
        release_note = parse_release_note(args.comment_file.read_text(encoding="utf-8"))
        updated_body = set_release_note(
            args.body_file.read_text(encoding="utf-8"), release_note
        )
    except ValueError as error:
        parser.exit(1, f"{error}\n")

    args.output_file.write_text(updated_body, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
