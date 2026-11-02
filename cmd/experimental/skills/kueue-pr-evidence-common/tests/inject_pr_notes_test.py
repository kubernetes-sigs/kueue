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

"""Offline tests for scripts/inject-pr-notes (no network, gh is stubbed).

Run from the repository root:
    python3 -B -m unittest discover -s cmd/experimental/skills/kueue-pr-evidence-common/tests -p '*_test.py'
"""

import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import stat
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

sys.dont_write_bytecode = True  # keep __pycache__ out of the skill tree

HERE = Path(__file__).resolve().parent
FIX = HERE / "fixtures"
SCRIPT = HERE.parent / "scripts" / "inject-pr-notes"

_loader = importlib.machinery.SourceFileLoader("inject_pr_notes", str(SCRIPT))
_spec = importlib.util.spec_from_loader("inject_pr_notes", _loader)
inj = importlib.util.module_from_spec(_spec)
_loader.exec_module(inj)

BASE = "a" * 40
HEAD = "b" * 40
SD = "kueue-pr-evidence-state-diff"
RG = "kueue-pr-evidence-red-green"
PD = "kueue-pr-evidence-perf-delta"


def fx(name: str) -> str:
    return (FIX / name).read_text(encoding="utf-8")


def release_note_tail(body: str) -> str:
    return body[body.index("#### Release note"):]


class RewriteBodyTest(unittest.TestCase):
    def test_template_golden(self):
        out = inj.rewrite_body(fx("template-empty-notes.txt"), SD, fx("block.txt"), BASE, HEAD)
        self.assertEqual(out, fx("template-empty-notes.expected.txt"))

    def test_idempotent(self):
        once = inj.rewrite_body(fx("template-empty-notes.txt"), SD, fx("block.txt"), BASE, HEAD)
        twice = inj.rewrite_body(once, SD, fx("block.txt"), BASE, HEAD)
        self.assertEqual(once, twice)

    def test_missing_section_created_before_release_note(self):
        body = fx("missing-section.txt")
        out = inj.rewrite_body(body, SD, "hello", BASE, HEAD)
        sec = out.index(inj.SECTION_HEADING)
        self.assertLess(out.index("Fix requeue after eviction."), sec)
        self.assertLess(sec, out.index("<!-- pr-evidence:kueue-pr-evidence-state-diff:start -->"))
        self.assertLess(out.index("<!-- pr-evidence:kueue-pr-evidence-state-diff:end -->"),
                        out.index("#### Release note"))
        self.assertEqual(release_note_tail(out), release_note_tail(body))

    def test_missing_section_no_heading_uses_release_note_fence(self):
        body = "Summary.\n\n```release-note\nNONE\n```\n"
        out = inj.rewrite_body(body, SD, "hello", BASE, HEAD)
        self.assertLess(out.index(inj.SECTION_HEADING), out.index("```release-note"))
        self.assertTrue(out.endswith("```release-note\nNONE\n```\n"))

    def test_empty_body(self):
        out = inj.rewrite_body("", SD, "hello", BASE, HEAD)
        self.assertTrue(out.startswith(inj.SECTION_HEADING + "\n\n<!-- pr-evidence:"))

    def test_existing_block_replaced_in_place(self):
        body = fx("existing-block.txt")
        out = inj.rewrite_body(body, SD, "NEW STATE DIFF", BASE, HEAD)
        self.assertNotIn("OLD STATE DIFF", out)
        self.assertEqual(out.count("pr-evidence:kueue-pr-evidence-state-diff:start"), 1)
        self.assertIn("**kueue-pr-evidence-state-diff** base aaaaaaaaaaaa -> head bbbbbbbbbbbb", out)
        # user text before and after the block is preserved, in order
        self.assertLess(out.index("Please look at the scheduler change first."), out.index("NEW STATE DIFF"))
        self.assertLess(out.index("NEW STATE DIFF"), out.index("Trailing user note."))
        self.assertEqual(release_note_tail(out), release_note_tail(body))

    def test_two_skills_coexist(self):
        body = fx("two-skills.txt")
        out = inj.rewrite_body(body, RG, "NEW RED GREEN", BASE, HEAD)
        self.assertIn("STATE DIFF", out)  # other skill untouched
        self.assertNotIn("\nRED GREEN\n", out)
        self.assertIn("NEW RED GREEN", out)
        self.assertLess(out.index("kueue-pr-evidence-state-diff:end"), out.index("kueue-pr-evidence-red-green:start"))

    def test_new_skill_appended_after_existing_blocks_not_inside(self):
        body = fx("two-skills.txt")
        out = inj.rewrite_body(body, PD, "PERF", BASE, HEAD)
        self.assertLess(out.index("kueue-pr-evidence-red-green:end"), out.index("kueue-pr-evidence-perf-delta:start"))
        self.assertLess(out.index("kueue-pr-evidence-perf-delta:end"), out.index("#### Release note"))
        self.assertEqual(release_note_tail(out), release_note_tail(body))

    def test_release_note_block_untouched(self):
        for name in ("template-empty-notes.txt", "missing-section.txt", "existing-block.txt", "two-skills.txt"):
            with self.subTest(name):
                body = fx(name)
                out = inj.rewrite_body(body, SD, "x", BASE, HEAD)
                self.assertEqual(release_note_tail(out), release_note_tail(body))

    def test_crlf_body_preserved(self):
        body = fx("existing-block.txt").replace("\n", "\r\n")
        out = inj.rewrite_body(body, SD, "NEW\nLINES", BASE, HEAD)
        self.assertNotIn("\n", out.replace("\r\n", ""))  # no bare LF introduced
        self.assertIn("NEW\r\nLINES", out)
        self.assertEqual(out, inj.rewrite_body(out, SD, "NEW\r\nLINES", BASE, HEAD))
        self.assertEqual(release_note_tail(out), release_note_tail(body))

    def test_crlf_missing_section(self):
        body = fx("missing-section.txt").replace("\n", "\r\n")
        out = inj.rewrite_body(body, SD, "x", BASE, HEAD)
        self.assertIn(inj.SECTION_HEADING + "\r\n", out)
        self.assertNotIn("\n", out.replace("\r\n", ""))

    def test_section_ends_at_deeper_release_note_heading(self):
        body = "### Useful notes for your reviewer\n\nnote\n\n#### Release note\n\n```release-note\nNONE\n```\n"
        out = inj.rewrite_body(body, SD, "x", BASE, HEAD)
        self.assertLess(out.index("kueue-pr-evidence-state-diff:end"), out.index("#### Release note"))
        self.assertEqual(release_note_tail(out), release_note_tail(body))

    def test_heading_in_code_fence_ignored(self):
        body = textwrap.dedent("""\
            #### Useful notes for your reviewer

            ```md
            #### Release note
            ```

            #### Release note

            ```release-note
            NONE
            ```
            """)
        out = inj.rewrite_body(body, SD, "x", BASE, HEAD)
        self.assertLess(out.index("```md"), out.index("kueue-pr-evidence-state-diff:start"))

    def test_malformed_markers_rejected(self):
        bad = {
            "unterminated": "<!-- pr-evidence:kueue-pr-evidence-state-diff:start -->\nx\n",
            "duplicate": ("<!-- pr-evidence:kueue-pr-evidence-state-diff:start -->\n"
                          "<!-- pr-evidence:kueue-pr-evidence-state-diff:end -->\n") * 2,
            "inline": "text <!-- pr-evidence:kueue-pr-evidence-state-diff:start -->\n",
            "nested": ("<!-- pr-evidence:kueue-pr-evidence-a:start -->\n<!-- pr-evidence:kueue-pr-evidence-b:start -->\n"
                       "<!-- pr-evidence:kueue-pr-evidence-b:end -->\n<!-- pr-evidence:kueue-pr-evidence-a:end -->\n"),
        }
        for name, body in bad.items():
            with self.subTest(name), self.assertRaises(inj.InjectError):
                inj.rewrite_body(body, SD, "x", BASE, HEAD)

    def test_input_validation(self):
        with self.assertRaises(inj.InjectError):
            inj.rewrite_body("", "pr-evidence-state-diff", "x", BASE, HEAD)
        with self.assertRaises(inj.InjectError):
            inj.rewrite_body("", SD, "x", "main", HEAD)
        with self.assertRaises(inj.InjectError):
            inj.rewrite_body("", SD, "<!-- pr-evidence:kueue-pr-evidence-x:start -->", BASE, HEAD)

    def test_header_pins_short_shas(self):
        self.assertEqual(inj.header_line(SD, "1a2b3c4d5e6f7a8b", "9f8e7d6c5b4a3b2c"),
                         "**kueue-pr-evidence-state-diff** base 1a2b3c4d5e6f -> head 9f8e7d6c5b4a")

    def test_visible_lines(self):
        self.assertEqual(inj.visible_lines(fx("block.txt")), 4)  # 3 table rows + <details> summary
        self.assertEqual(inj.visible_lines("a\n\nb\n<details>\n1\n2\n</details>\nc"), 4)


class CliTest(unittest.TestCase):
    """CLI paths with a stub `gh` on PATH that records invocations."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.log = self.dir / "gh.log"
        self.block = self.dir / "block.md"
        self.block.write_text("hello\n", encoding="utf-8")
        self.old_path = os.environ.get("PATH", "")

    def tearDown(self):
        os.environ["PATH"] = self.old_path
        self.tmp.cleanup()

    def stub_gh(self, author="me", login="me", head=HEAD, body=None):
        pr = {"body": body if body is not None else fx("template-empty-notes.txt"),
              "author": {"login": author}, "headRefOid": head,
              "url": "https://github.com/kubernetes-sigs/kueue/pull/1", "state": "OPEN"}
        (self.dir / "pr.json").write_text(json.dumps(pr), encoding="utf-8")
        gh = self.dir / "gh"
        gh.write_text(textwrap.dedent(f"""\
            #!/bin/sh
            echo "$*" >> "{self.log}"
            case "$1 $2" in
              "pr view") cat "{self.dir / 'pr.json'}" ;;
              "api user") echo "{login}" ;;
              "pr edit") cp "$7" "{self.dir / 'edited.md'}" ;;
              *) echo "unexpected gh $*" >&2; exit 9 ;;
            esac
            """), encoding="utf-8")
        gh.chmod(gh.stat().st_mode | stat.S_IEXEC)
        os.environ["PATH"] = f"{self.dir}{os.pathsep}{self.old_path}"

    def run_cli(self, *extra):
        out, err = io.StringIO(), io.StringIO()
        argv = ["--pr", "https://github.com/kubernetes-sigs/kueue/pull/1", "--skill", SD,
                "--file", str(self.block), "--base", BASE, "--head", HEAD, *extra]
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = inj.main(argv)
        return rc, out.getvalue(), err.getvalue()

    def gh_calls(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def test_dry_run_prints_diff_and_never_edits(self):
        self.stub_gh()
        rc, out, _ = self.run_cli("--dry-run")
        self.assertEqual(rc, 0)
        self.assertIn("+<!-- pr-evidence:kueue-pr-evidence-state-diff:start -->", out)
        self.assertFalse(any(c.startswith("pr edit") for c in self.gh_calls()))

    def test_write_uses_body_file(self):
        self.stub_gh()
        rc, out, _ = self.run_cli()
        self.assertEqual(rc, 0, out)
        edit = [c for c in self.gh_calls() if c.startswith("pr edit")]
        self.assertEqual(len(edit), 1)
        self.assertIn("--repo kubernetes-sigs/kueue --body-file", edit[0])
        self.assertIn("hello", (self.dir / "edited.md").read_text())

    def test_refuses_other_author(self):
        self.stub_gh(author="someone-else")
        rc, _, err = self.run_cli()
        self.assertEqual(rc, 3)
        self.assertIn("--allow-other-author", err)
        self.assertFalse(any(c.startswith("pr edit") for c in self.gh_calls()))
        rc, _, _ = self.run_cli("--allow-other-author")
        self.assertEqual(rc, 0)

    def test_refuses_stale_head(self):
        self.stub_gh(head="c" * 40)
        rc, _, err = self.run_cli()
        self.assertEqual(rc, 3)
        self.assertIn("push first", err)

    def test_no_change_skips_edit(self):
        body = inj.rewrite_body(fx("template-empty-notes.txt"), SD, "hello\n", BASE, HEAD)
        self.stub_gh(body=body)
        rc, out, _ = self.run_cli()
        self.assertEqual(rc, 0)
        self.assertIn("no change", out)
        self.assertFalse(any(c.startswith("pr edit") for c in self.gh_calls()))

    def test_visible_line_budget(self):
        self.stub_gh()
        self.block.write_text("\n".join(f"line {i}" for i in range(30)), encoding="utf-8")
        rc, _, err = self.run_cli("--dry-run")
        self.assertEqual(rc, 2)
        self.assertIn("<details>", err)

    def test_missing_pr_is_usage_error(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            rc = inj.main(["--skill", SD, "--file", str(self.block), "--base", BASE, "--head", HEAD])
        self.assertEqual(rc, 2)
        self.assertIn("--pr is required", err.getvalue())

    def test_gh_failure_exit_code(self):
        os.environ["PATH"] = str(self.dir)  # no gh at all
        rc, _, err = self.run_cli("--dry-run")
        self.assertEqual(rc, 1)
        self.assertIn("gh CLI not found", err)

    def test_dry_run_reports_refusals_without_failing(self):
        self.stub_gh(author="someone-else", head="c" * 40)
        rc, out, err = self.run_cli("--dry-run")
        self.assertEqual(rc, 0)
        self.assertIn("would refuse", err)
        self.assertIn("+<!-- pr-evidence:", out)

    def test_offline_body_in_write_prints_new_body(self):
        os.environ["PATH"] = str(self.dir)
        body_in = self.dir / "body.md"
        body_in.write_text(fx("template-empty-notes.txt"), encoding="utf-8")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = inj.main(["--skill", SD, "--file", str(FIX / "block.txt"), "--base", BASE, "--head", HEAD,
                           "--body-in", str(body_in)])
        self.assertEqual(rc, 0)
        self.assertEqual(out.getvalue(), fx("template-empty-notes.expected.txt"))

    def test_offline_body_in_never_calls_gh(self):
        os.environ["PATH"] = str(self.dir)  # no gh at all
        body_in = self.dir / "body.md"
        body_in.write_text(fx("missing-section.txt"), encoding="utf-8")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = inj.main(["--skill", SD, "--file", str(self.block), "--base", BASE, "--head", HEAD,
                           "--body-in", str(body_in), "--dry-run"])
        self.assertEqual(rc, 0)
        self.assertIn("+" + inj.SECTION_HEADING, out.getvalue())


if __name__ == "__main__":
    unittest.main()
