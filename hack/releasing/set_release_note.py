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
# Prow commands enabled for this repository, see
# https://prow.k8s.io/command-help?repo=kubernetes-sigs%2Fkueue
PROW_COMMANDS = frozenset(
    {
        "approve",
        "area",
        "assign",
        "auto-cc",
        "bark",
        "bugzilla",
        "buildifier",
        "buildify",
        "cc",
        "check-cla",
        "check-dco",
        "check-required-labels",
        "cherry-pick",
        "cherrypick",
        "close",
        "committee",
        "good-first-issue",
        "help",
        "hold",
        "honk",
        "joke",
        "kind",
        "label",
        "language",
        "lgtm",
        "lifecycle",
        "link-issue",
        "lint",
        "meow",
        "meowvie",
        "milestone",
        "ok-to-test",
        "override",
        "override-cancel",
        "override-sticky",
        "pin-issue",
        "pony",
        "priority",
        "project",
        "release-note-edit",
        "release-note-none",
        "reopen",
        "retest",
        "retest-required",
        "retitle",
        "shrug",
        "sig",
        "skip",
        "stage",
        "status",
        "test",
        "test-manual-required",
        "this-is-fine",
        "this-is-not-fine",
        "this-is-unbearable",
        "transfer",
        "transfer-issue",
        "triage",
        "trick-or-treat",
        "trickortreat",
        "unassign",
        "uncc",
        "unhold",
        "unlink-issue",
        "unpin-issue",
        "unshrug",
        "verify-owners",
        "wg",
        "woof",
    }
)
# A line holding a single command, such as "/kind bug" or "/lgtm". Prow only
# matches a command at the start of a line and ignores the case of its name.
COMMAND_LINE_RE = re.compile(r"^/(?P<name>[a-z][a-z0-9-]*)(?:[ \t]+\S.*)?$", re.IGNORECASE)
FENCE_RE = re.compile(
    r"^(?P<indent> {0,3})(?P<fence>`{3,}|~{3,})(?P<info>[^\r\n]*?)[ \t]*(?:\r?\n)?$"
)


def _match_fence(line: str) -> re.Match[str] | None:
    """Match a CommonMark fence line, including its info-string restriction."""
    match = FENCE_RE.fullmatch(line)
    if (
        match is not None
        and match.group("fence")[0] == "`"
        and "`" in match.group("info")
    ):
        return None
    return match


def _is_set_release_note_line(line: str) -> bool:
    """Report whether the line is the /set-release-note command."""
    return line.rstrip().lower() == COMMAND


# Prow commands that also change the release note. Combining one of them with
# /set-release-note in the same comment is contradictory.
CONFLICTING_COMMANDS = frozenset({"release-note-edit", "release-note-none"})


def _command_name(line: str) -> str | None:
    """Return the lowercase command name of a command line, or None."""
    match = COMMAND_LINE_RE.match(line.rstrip())
    return match.group("name").lower() if match else None


def _is_prow_command_line(line: str) -> bool:
    """Report whether the line is a Prow command, such as "/kind bug"."""
    name = _command_name(line)
    if name is None:
        return False
    # The label plugin accepts "/remove-<label>" for any label prefix.
    return name in PROW_COMMANDS or name.startswith("remove-")


def parse_release_note(comment: str) -> str:
    """Parse and validate the release note from a ChatOps comment.

    The note is the text after the last /set-release-note line, up to the next
    Prow command line such as "/kind bug". Other commands in the same comment
    are handled by Prow and must not end up in the release note. Other lines
    that start with "/" are kept in the note.
    """
    lines = comment.replace("\r\n", "\n").splitlines()
    last_command = next(
        (i for i in reversed(range(len(lines))) if _is_set_release_note_line(lines[i])),
        None,
    )
    if last_command is None:
        raise ValueError(f"{COMMAND} must be on a line of its own.")
    if any(_command_name(line) in CONFLICTING_COMMANDS for line in lines):
        raise ValueError(
            f"{COMMAND} cannot be combined with /release-note-edit or /release-note-none."
        )

    note_lines = []
    for line in lines[last_command + 1 :]:
        if _is_prow_command_line(line):
            break
        note_lines.append(line)
    release_note = "\n".join(note_lines).strip()
    if not release_note:
        raise ValueError(f"{COMMAND} must be followed by a non-empty release note.")
    if any(_match_fence(line) for line in release_note.splitlines()):
        raise ValueError("The release note must not contain a fenced code block.")
    return release_note


def _line_content_end(line: str) -> int:
    """Return the offset before a line's newline sequence."""
    return len(line.rstrip("\r\n"))


def _release_note_spans(pr_body: str) -> list[tuple[int, int]]:
    """Find top-level release-note spans without consuming malformed content."""
    lines = pr_body.splitlines(keepends=True)
    offsets: list[int] = []
    offset = 0
    for line in lines:
        offsets.append(offset)
        offset += len(line)

    spans: list[tuple[int, int]] = []
    line_index = 0
    while line_index < len(lines):
        opening = _match_fence(lines[line_index])
        if opening is None:
            line_index += 1
            continue

        fence = opening.group("fence")
        fence_char = fence[0]
        fence_length = len(fence)
        info = opening.group("info").strip()
        is_release_note = fence_char == "`" and info == "release-note"

        closing_index: int | None = None
        nested_fence_index: int | None = None
        for candidate_index in range(line_index + 1, len(lines)):
            candidate = _match_fence(lines[candidate_index])
            if candidate is None:
                continue

            candidate_fence = candidate.group("fence")
            candidate_info = candidate.group("info").strip()
            if (
                candidate_info == ""
                and candidate_fence[0] == fence_char
                and len(candidate_fence) >= fence_length
            ):
                closing_index = candidate_index
                break
            if (
                is_release_note
                and candidate_fence[0] == fence_char
                and len(candidate_fence) < fence_length
            ):
                continue
            if is_release_note and candidate_fence[0] == fence_char:
                nested_fence_index = candidate_index
                break

        if is_release_note:
            start = offsets[line_index]
            if closing_index is None:
                end = start + _line_content_end(lines[line_index])
            else:
                end = offsets[closing_index] + _line_content_end(lines[closing_index])
            spans.append((start, end))

        if closing_index is not None:
            line_index = closing_index + 1
        elif nested_fence_index is not None:
            line_index = nested_fence_index
        elif not is_release_note:
            line_index = len(lines)
        else:
            line_index += 1

    return spans


def set_release_note(pr_body: str, release_note: str) -> str:
    """Replace an existing release-note block or append a standard block.

    A well-formed block is replaced while the surrounding text is preserved:

    >>> set_release_note(
    ...     "Summary\\n\\n```release-note\\nold note\\n```\\n\\nDetails\\n",
    ...     "new note",
    ... )
    'Summary\\n\\n```release-note\\nnew note\\n```\\n\\nDetails\\n'

    For an unclosed block, only the opening fence is replaced. The text below
    the fence is preserved outside the new block rather than being discarded:

    >>> set_release_note(
    ...     "Summary\\n\\n```release-note\\nold note\\n\\nDetails\\n",
    ...     "new note",
    ... )
    'Summary\\n\\n```release-note\\nnew note\\n```\\nold note\\n\\nDetails\\n'
    """
    spans = _release_note_spans(pr_body)
    if len(spans) > 1:
        raise ValueError("The pull request body contains multiple release-note blocks.")

    new_block = f"```release-note\n{release_note}\n```"
    if spans:
        start, end = spans[0]
        updated_body = f"{pr_body[:start]}{new_block}{pr_body[end:]}"
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
