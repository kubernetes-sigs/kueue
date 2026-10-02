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
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import set_release_note


class ParseReleaseNoteTest(unittest.TestCase):
    def test_valid_comments(self) -> None:
        test_cases = {
            "single-line note": (
                "/set-release-note\nA useful change",
                "A useful change",
            ),
            "multiline note": (
                "/set-release-note\nFirst line\nSecond line",
                "First line\nSecond line",
            ),
            "CRLF line endings": (
                "/set-release-note\r\nFirst line\r\nSecond line\r\n",
                "First line\nSecond line",
            ),
        }

        for name, (comment, want) in test_cases.items():
            with self.subTest(name=name):
                self.assertEqual(want, set_release_note.parse_release_note(comment))

    def test_invalid_comments(self) -> None:
        test_cases = {
            "empty comment": (
                "",
                "/set-release-note must be the first line of the comment.",
            ),
            "command after other text": (
                "Please update this.\n/set-release-note\nA useful change",
                "/set-release-note must be the first line of the comment.",
            ),
            "command with surrounding whitespace": (
                "  /set-release-note  \nA useful change",
                "/set-release-note must be the first line of the comment.",
            ),
            "missing note": (
                "/set-release-note",
                "/set-release-note must be followed by a non-empty release note.",
            ),
            "whitespace-only note": (
                "/set-release-note\n \t\n",
                "/set-release-note must be followed by a non-empty release note.",
            ),
            "fenced block in note": (
                "/set-release-note\n```text\nA useful change\n```",
                "The release note must not contain a fenced code block.",
            ),
            "tilde-fenced block in note": (
                "/set-release-note\n~~~text\nA useful change\n~~~",
                "The release note must not contain a fenced code block.",
            ),
        }

        for name, (comment, want_error) in test_cases.items():
            with self.subTest(name=name):
                with self.assertRaisesRegex(ValueError, re.escape(want_error)):
                    set_release_note.parse_release_note(comment)


class SetReleaseNoteTest(unittest.TestCase):
    def test_updates_pull_request_body(self) -> None:
        test_cases = {
            "replace existing block": (
                "Summary\n\n```release-note\nold note\n```\n\nDetails\n",
                "new note",
                "Summary\n\n```release-note\nnew note\n```\n\nDetails\n",
            ),
            "replace unclosed block opening": (
                "Summary\n\n```release-note\nold note\n\nDetails\n",
                "new note",
                "Summary\n\n```release-note\nnew note\n```\nold note\n\nDetails\n",
            ),
            "append to non-empty body": (
                "Summary\n",
                "new note",
                "Summary\n\n```release-note\nnew note\n```\n",
            ),
            "add to empty body": (
                " \n\t",
                "new note",
                "```release-note\nnew note\n```\n",
            ),
            "preserve replacement text literally": (
                "```release-note\nold note\n```\n",
                r"Use $1 and \\1 literally",
                "```release-note\nUse $1 and \\\\1 literally\n```\n",
            ),
            "preserve a structured block after an unclosed release note": (
                "Summary\n\n```release-note\nold note\n\nDetails\n"
                "```go\nfmt.Println(1)\n```\n\nTail\n",
                "new note",
                "Summary\n\n```release-note\nnew note\n```\nold note\n\nDetails\n"
                "```go\nfmt.Println(1)\n```\n\nTail\n",
            ),
            "preserve a tilde block after an unclosed release note": (
                "```release-note\nold note\n~~~go\nfmt.Println(1)\n~~~\n",
                "new note",
                "```release-note\nnew note\n```\nold note\n"
                "~~~go\nfmt.Println(1)\n~~~\n",
            ),
            "accept a longer closing fence": (
                "Summary\n\n```release-note\nold note\n````\n\nDetails\n",
                "new note",
                "Summary\n\n```release-note\nnew note\n```\n\nDetails\n",
            ),
            "replace a longer block containing a shorter fence": (
                "````release-note\nold note\n```\nmore old note\n````\nTail\n",
                "new note",
                "```release-note\nnew note\n```\nTail\n",
            ),
            "replace a block containing a different fence character": (
                "```release-note\nold note\n~~~yaml\nkey: value\n~~~\n```\nTail\n",
                "new note",
                "```release-note\nnew note\n```\nTail\n",
            ),
            "replace a three-space-indented block": (
                "   ```release-note\nold note\n   ```\n",
                "new note",
                "```release-note\nnew note\n```\n",
            ),
            "preserve CRLF around a replacement": (
                "Summary\r\n\r\n```release-note\r\nold note\r\n```\r\nTail\r\n",
                "new note",
                "Summary\r\n\r\n```release-note\nnew note\n```\r\nTail\r\n",
            ),
            "ignore release note example in an outer fence": (
                "````markdown\n```release-note\nexample\n```\n````\n",
                "new note",
                "````markdown\n```release-note\nexample\n```\n````\n\n"
                "```release-note\nnew note\n```\n",
            ),
            "ignore an invalid backtick info string": (
                "```bad`info\ntext\n```release-note\nold note\n```\n",
                "new note",
                "```bad`info\ntext\n```release-note\nnew note\n```\n",
            ),
            "ignore four-space-indented release note example": (
                "    ```release-note\n    example\n    ```\n",
                "new note",
                "    ```release-note\n    example\n    ```\n\n"
                "```release-note\nnew note\n```\n",
            ),
        }

        for name, (pr_body, release_note, want) in test_cases.items():
            with self.subTest(name=name):
                self.assertEqual(
                    want, set_release_note.set_release_note(pr_body, release_note)
                )

    def test_rejects_multiple_release_note_blocks(self) -> None:
        pr_body = (
            "```release-note\nfirst note\n```\n\n"
            "```release-note\nsecond note\n```\n"
        )

        with self.assertRaisesRegex(
            ValueError,
            "The pull request body contains multiple release-note blocks.",
        ):
            set_release_note.set_release_note(pr_body, "new note")

    def test_enforces_updated_body_length_limit(self) -> None:
        pr_body = "Summary"
        release_note = "new note"
        updated_body = "Summary\n\n```release-note\nnew note\n```\n"

        with mock.patch.object(
            set_release_note, "MAX_PR_BODY_LENGTH", len(updated_body)
        ):
            self.assertEqual(
                updated_body, set_release_note.set_release_note(pr_body, release_note)
            )

        with mock.patch.object(
            set_release_note, "MAX_PR_BODY_LENGTH", len(updated_body) - 1
        ):
            with self.assertRaisesRegex(
                ValueError, "The updated pull request body exceeds"
            ):
                set_release_note.set_release_note(pr_body, release_note)


class MainTest(unittest.TestCase):
    def test_writes_updated_body(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            comment_file = temp_path / "comment.md"
            body_file = temp_path / "body.md"
            output_file = temp_path / "output.md"
            comment_file.write_text(
                "/set-release-note\nnew note", encoding="utf-8"
            )
            body_file.write_text("Summary\n", encoding="utf-8")

            with mock.patch.object(
                sys,
                "argv",
                [
                    "set_release_note.py",
                    "--comment-file",
                    str(comment_file),
                    "--body-file",
                    str(body_file),
                    "--output-file",
                    str(output_file),
                ],
            ):
                self.assertEqual(0, set_release_note.main())

            self.assertEqual(
                "Summary\n\n```release-note\nnew note\n```\n",
                output_file.read_text(encoding="utf-8"),
            )

    def test_reports_validation_error(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            comment_file = temp_path / "comment.md"
            body_file = temp_path / "body.md"
            output_file = temp_path / "output.md"
            comment_file.write_text("/set-release-note", encoding="utf-8")
            body_file.write_text("Summary\n", encoding="utf-8")
            stderr = io.StringIO()

            with mock.patch.object(
                sys,
                "argv",
                [
                    "set_release_note.py",
                    "--comment-file",
                    str(comment_file),
                    "--body-file",
                    str(body_file),
                    "--output-file",
                    str(output_file),
                ],
            ):
                with contextlib.redirect_stderr(stderr):
                    with self.assertRaises(SystemExit) as got:
                        set_release_note.main()

            self.assertEqual(1, got.exception.code)
            self.assertIn(
                "/set-release-note must be followed by a non-empty release note.",
                stderr.getvalue(),
            )
            self.assertFalse(output_file.exists())


if __name__ == "__main__":
    unittest.main()
