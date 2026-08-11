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

import unittest

from set_release_note import MAX_PR_BODY_LENGTH, parse_release_note, set_release_note


class ParseReleaseNoteTest(unittest.TestCase):
    def test_valid_comment(self) -> None:
        cases = {
            "single-line note": (
                "/set-release-note\nAdd queue support.",
                "Add queue support.",
            ),
            "multiline note with CRLF": (
                "/set-release-note\r\nFirst line.\r\nSecond line.\r\n",
                "First line.\nSecond line.",
            ),
        }

        for name, (comment, want) in cases.items():
            with self.subTest(name):
                self.assertEqual(want, parse_release_note(comment))

    def test_invalid_comment(self) -> None:
        cases = {
            "command is not first": (
                "prefix\n/set-release-note\nNote",
                "/set-release-note must be the first line of the comment.",
            ),
            "note is empty": (
                "/set-release-note\n  ",
                "/set-release-note must be followed by a non-empty release note.",
            ),
            "note contains fenced block": (
                "/set-release-note\n```text\nnote\n```",
                "The release note must not contain a fenced code block.",
            ),
        }

        for name, (comment, want_error) in cases.items():
            with self.subTest(name):
                with self.assertRaisesRegex(ValueError, want_error):
                    parse_release_note(comment)


class SetReleaseNoteTest(unittest.TestCase):
    def test_updates_body(self) -> None:
        cases = {
            "replace existing block": (
                "Before\n\n```release-note\nOld note\n```\n\nAfter\n",
                "New note",
                "Before\n\n```release-note\nNew note\n```\n\nAfter\n",
            ),
            "repair unclosed block": (
                "Before\n\n```release-note\nOld note\n\n",
                "New note",
                "Before\n\n```release-note\nNew note\n```\n",
            ),
            "append to non-empty body": (
                "Before\n",
                "New note",
                "Before\n\n```release-note\nNew note\n```\n",
            ),
            "append to empty body": (
                "",
                "New note",
                "```release-note\nNew note\n```\n",
            ),
        }

        for name, (pr_body, release_note, want) in cases.items():
            with self.subTest(name):
                self.assertEqual(want, set_release_note(pr_body, release_note))

    def test_multiple_blocks_are_rejected(self) -> None:
        cases = {
            "two closed blocks": """\
```release-note
First
```

```release-note
Second
```
""",
            "closed and unclosed blocks": """\
```release-note
First
```

```release-note
Second
""",
            "two unclosed blocks": """\
```release-note
First

```release-note
Second
""",
        }

        for name, pr_body in cases.items():
            with self.subTest(name):
                with self.assertRaisesRegex(
                    ValueError,
                    "The pull request body contains multiple release-note blocks.",
                ):
                    set_release_note(pr_body, "New note")

    def test_maximum_body_length_is_enforced(self) -> None:
        block_without_note = "```release-note\n\n```\n"
        release_note = "x" * (MAX_PR_BODY_LENGTH - len(block_without_note))

        updated_body = set_release_note("", release_note)

        self.assertEqual(MAX_PR_BODY_LENGTH, len(updated_body))

        with self.assertRaisesRegex(
            ValueError,
            f"The updated pull request body exceeds {MAX_PR_BODY_LENGTH} characters.",
        ):
            set_release_note("", f"{release_note}x")

    def test_replacement_at_maximum_body_length(self) -> None:
        new_block = "```release-note\nNew note\n```"
        prefix = "x" * (MAX_PR_BODY_LENGTH - len(new_block) - 1) + "\n"
        pr_body = f"{prefix}```release-note\nOld note\n```"

        updated_body = set_release_note(pr_body, "New note")

        self.assertEqual(MAX_PR_BODY_LENGTH, len(updated_body))


if __name__ == "__main__":
    unittest.main()
