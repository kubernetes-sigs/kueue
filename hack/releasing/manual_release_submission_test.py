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

"""Exercise PR submission and issue updates without preparing branches or contacting GitHub."""

import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
TEMPLATE = (REPO_ROOT / ".github/ISSUE_TEMPLATE/NEW_RELEASE.md").read_text()


class ManualReleaseSubmissionTest(unittest.TestCase):
    def test_submission_and_checklist_control_flow(self):
        harness = '''
set -o errexit
set -o nounset
set -o pipefail
function prepare_local_branch() { printf '["prepare"]\\n' >> "$GH_CALLS"; }
function git() {
  case "$1" in
    checkout|branch) ;;
    *) printf '["git", "%s"]\\n' "$1" >> "$GH_CALLS"; return "$PUSH_STATUS" ;;
  esac
}
function gh() { "$TEST_PYTHON" "$GH_STUB" "$@"; }
function make_pr() { gh pr create; }
'''
        scripts = {
            "prepare all": ("prepare_pull.sh", 'if [[ "$TARGET" == "all"', "all",
                            {"prepare-pull-release", "prepare-pull-main"}),
            "prepare release": ("prepare_pull.sh", 'if [[ "$TARGET" == "all"', "release",
                                {"prepare-pull-release"}),
            "prepare main": ("prepare_pull.sh", 'if [[ "$TARGET" == "all"', "main",
                             {"prepare-pull-main"}),
            "promote": ("promote_pull.sh", 'K8S_IO_BRANCH="', "", {"promote-pull"}),
            "CI": ("ci_pull.sh", 'CI_BRANCH="', "", {"ci-pull"}),
        }
        prepared = ["prepare", "submit"]
        submitted = prepared + ["git push", "pr create", "pr list", "issue edit"]
        cases = {
            "confirmed": ({}, "y\ny\n", 0, submitted, True),
            "uppercase confirmation": ({}, "Y\nY\n", 0, submitted, True),
            "declined": ({}, "n\n", 1, prepared, False),
            "uppercase decline": ({}, "N\n", 1, prepared, False),
            "default answer": ({}, "\n", 1, prepared, False),
            "invalid answer": ({}, "yes\n", 1, prepared, False),
            "closed stdin": ({}, "", 1, prepared, False),
            "push failed": ({"PUSH_STATUS": "1"}, "y\n", 1, prepared + ["git push"], False),
            "PR creation failed": ({"CREATE_STATUS": "1"}, "y\n", 1, prepared + ["git push", "pr create"], False),
            "dry run": ({"DRY_RUN": "1"}, "", 0, ["prepare"], False),
            "PR not found": ({"NO_PR": "1"}, "y\ny\n", 0, submitted[:-1], False),
        }
        for script_case, (script_name, start, target, want_steps) in scripts.items():
            phases = 2 if target == "all" else 1
            source = (REPO_ROOT / "hack/releasing" / script_name).read_text()
            match = re.search(r"^function push_and_create_pr\(\) \{.*?^\}",
                              source, re.MULTILINE | re.DOTALL)
            self.assertIsNotNone(match, f"push_and_create_pr not found in {script_name}")
            push_function = match.group().replace("{\n", '{\n  printf \'["submit"]\\n\' >> "$GH_CALLS"\n', 1)
            script = push_function + "\n" + source[source.index(start):]
            if script_name == "prepare_pull.sh":
                # Keep the real initialization, cleanup and main phases in source order.
                # Only branch preparation is mocked, since it changes tracked files.
                script = source[source.index("clean_branches=()"):]
                script = script.replace(match.group(), push_function, 1)
                prepare_function = '''function prepare_local_branch() {
  printf '["prepare"]\\n' >> "$GH_CALLS"
  printf 'raise SystemExit("old release helper has no checklist CLI")\\n' > "$REPO_ROOT/hack/releasing/mark_step_done.py"
}'''
                script = re.sub(r"^function prepare_local_branch\(\) \{.*?^\}", lambda _: prepare_function,
                                script, count=1, flags=re.MULTILINE | re.DOTALL)
            for name, (env, answers, want_status, want_calls, want_edit) in cases.items():
                with self.subTest(script=script_case, case=name), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    helper = root / "hack/releasing/mark_step_done.py"
                    helper.parent.mkdir(parents=True)
                    helper.write_text((REPO_ROOT / "hack/releasing/mark_step_done.py").read_text())
                    calls = root / "calls.jsonl"
                    gh = root / "gh.py"
                    gh.write_text('''
import json
import os
import sys
from pathlib import Path

args = sys.argv[1:]
with Path(os.environ["GH_CALLS"]).open("a") as calls:
    calls.write(json.dumps(args) + "\\n")
if args[:2] == ["pr", "create"]:
    sys.exit(int(os.environ["CREATE_STATUS"]))
if args[:2] == ["pr", "list"] and not os.environ.get("NO_PR"):
    for title in ("Prepare release v0.20.0", "Update main with the latest v0.20.0",
                  "Kueue: Promote v0.20.0", "Kueue: CI for 0.20"):
        print("42\\t" + title)
''')
                    test_env = {
                        **os.environ,
                        "REPO_ROOT": str(root),
                        "KUBERNETES_SIGS_KUEUE_PATH": str(REPO_ROOT),
                        "RELEASE_ISSUE_BODY": TEMPLATE,
                        "RELEASE_ISSUE_NUMBER": "1",
                        "RELEASE_ISSUE_NAME": "Release v0.20.0",
                        "RELEASE_VERSION": "v0.20.0",
                        "RELEASE_BRANCH": "release-0.20",
                        "MAJOR_MINOR": "0.20",
                        "TARGET": target,
                        "MAIN_REPO_ORG": "kubernetes-sigs",
                        "MAIN_REPO_NAME": "kueue",
                        "KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG": "kubernetes-sigs",
                        "KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME": "kueue",
                        "KUBERNETES_SIGS_KUEUE_MAIN_REPO": "kubernetes-sigs/kueue",
                        "KUBERNETES_TEST_INFRA_MAIN_REPO_ORG": "kubernetes",
                        "KUBERNETES_TEST_INFRA_MAIN_REPO_NAME": "test-infra",
                        "KUBERNETES_K8S_IO_MAIN_REPO": "kubernetes/k8s.io",
                        "GITHUB_USER": "release-test",
                        "FORK_REMOTE": "origin",
                        "KUBERNETES_TEST_INFRA_FORK_REMOTE": "origin",
                        "KUBERNETES_K8S_IO_FORK_REMOTE": "origin",
                        "DRY_RUN": "",
                        "PUSH_STATUS": "0",
                        "CREATE_STATUS": "0",
                        "NO_PR": "",
                        "GH_CALLS": str(calls),
                        "GH_STUB": str(gh),
                        "TEST_PYTHON": sys.executable,
                        "STARTING_BRANCH": "main",
                        **env,
                    }
                    result = subprocess.run(["bash", "-c", harness + script],
                                            env=test_env, cwd=root, input=answers,
                                            capture_output=True, text=True, check=False)
                    self.assertEqual(result.returncode, want_status, result.stdout + result.stderr)
                    self.assertEqual(result.stdout.count("Skipping git push"),
                                     phases if test_env["DRY_RUN"] else 0)
                    recorded = [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else []
                    expected_calls = want_calls * phases if want_status == 0 else want_calls
                    self.assertEqual([" ".join(args[:2]) for args in recorded], expected_calls)
                    edits = [args for args in recorded if args[:2] == ["issue", "edit"]]
                    self.assertEqual(bool(edits), want_edit, recorded)
                    if want_edit:
                        for index, args in enumerate(edits):
                            body = args[args.index("--body") + 1]
                            checked = set(re.findall(r"- \[x\][^\n]*<!-- step:([a-z-]+) -->", body))
                            steps = {"prepare-pull-release"} if phases == 2 and index == 0 else want_steps
                            self.assertEqual(checked, steps)
                            self.assertNotIn("- [x] Wait for", body)

    def test_milestone_and_release_notes_update_only_the_completed_step(self):
        milestone = (REPO_ROOT / "hack/releasing/milestone_pull.sh").read_text()
        update_function = re.search(r"^function update_release_issue\(\) \{.*?^\}",
                                    milestone, re.MULTILINE | re.DOTALL).group()
        notes = (REPO_ROOT / "hack/releasing/sync-notes.sh").read_text()
        notes_phase = notes[notes.index('read -p "+++ Do you want to update release issue?'):]
        phases = {
            "milestone-pull": update_function + '\nupdate_release_issue owner/repo kubernetes/test-infra https://github.com/kubernetes/test-infra/pull/42\n',
            "sync-release-notes": notes_phase,
        }
        log = "\n<!-- release-log-start -->\nexisting release log\n<!-- release-log-end -->\n"
        cases = {
            "success": (TEMPLATE + log, "y\n", True, True),
            "older template": (re.sub(r" <!-- step:[a-z-]+ -->", "", TEMPLATE) + log,
                               "y\n", True, False),
            "declined": (TEMPLATE + log, "n\n", False, False),
            "closed stdin": (TEMPLATE + log, "", False, False),
        }
        for command, phase in phases.items():
            for name, (body, answer, want_success, want_checked) in cases.items():
                # milestone's confirmation is covered by milestone_pull_test.sh;
                # update_release_issue is called only after PR creation.
                if command == "milestone-pull" and name in {"declined", "closed stdin"}:
                    continue
                with self.subTest(command=command, case=name), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    gh = root / "gh.py"
                    updated_body = root / "updated.txt"
                    changelog = root / "changelog.md"
                    changelog.write_text("- New release note\n")
                    gh.write_text('''
import json
import os
import sys
from pathlib import Path

args = sys.argv[1:]
if args[:2] == ["issue", "list"]:
    print("1\\tRelease v0.20.0")
elif args[:2] == ["issue", "view"]:
    print(json.dumps({"body": os.environ["TEST_ISSUE_BODY"]}))
elif args[:2] == ["issue", "edit"]:
    Path(os.environ["UPDATED_BODY"]).write_text(args[args.index("--body") + 1])
else:
    raise AssertionError(args)
''')
                    env = {
                        **os.environ,
                        "REPO_ROOT": str(REPO_ROOT),
                        "KUBERNETES_SIGS_KUEUE_PATH": str(REPO_ROOT),
                        "RELEASE_ISSUE_NUMBER": "1",
                        "RELEASE_ISSUE_NAME": "Release v0.20.0",
                        "RELEASE_VERSION": "v0.20.0",
                        "MAIN_REPO_ORG": "owner",
                        "MAIN_REPO_NAME": "repo",
                        "PREVIOUS_VERSION": "v0.19.0",
                        "FINAL_CHANGELOG_FILE": str(changelog),
                        "TEST_ISSUE_BODY": body,
                        "UPDATED_BODY": str(updated_body),
                        "GH_STUB": str(gh),
                        "TEST_PYTHON": sys.executable,
                    }
                    harness = '''
set -o errexit
set -o nounset
set -o pipefail
function gh() { "$TEST_PYTHON" "$GH_STUB" "$@"; }
'''
                    result = subprocess.run(["bash", "-c", harness + phase], env=env, cwd=root,
                                            input=answer, capture_output=True, text=True, check=False)
                    self.assertEqual(result.returncode == 0, want_success, result.stdout + result.stderr)
                    self.assertEqual(updated_body.exists(), want_success)
                    if want_success:
                        updated = updated_body.read_text()
                        checked = set(re.findall(r"- \[x\][^\n]*<!-- step:([a-z-]+) -->", updated))
                        self.assertEqual(checked, {command} if want_checked else set())
                        self.assertTrue(updated.endswith(log.rstrip("\n")))
                        self.assertNotIn("- [x] Wait for", updated)
                        if command == "sync-release-notes":
                            self.assertIn("- New release note", updated)


if __name__ == "__main__":
    unittest.main()
