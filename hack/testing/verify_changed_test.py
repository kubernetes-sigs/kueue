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

"""Exercise selection and execution in disposable Git repositories."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SELECTOR = Path(__file__).with_name("verify_changed.py")


class ChangedVerificationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.root = self.directory / "repo"
        self.root.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Verification Test")
        for path in (
            "cmd/kueuectl/main.go", "pkg/shared/shared.go", "pkg/linux/client.go",
            "pkg/scheduler/scheduler.go", "apis/config/v1beta2/defaults.go",
            "apis/config/v1beta2/defaults_test.go", "docs.md", "hack/testing/e2e-common.sh",
            "site/generated.md", "go.mod",
        ):
            self.write(path, "original\n")
        self.git("add", ".")
        self.git("commit", "-m", "Initial fixture")
        self.git("switch", "-c", "feature")
        self.go = self.directory / "go"
        self.go.write_text('''#!/usr/bin/env python3
import os
from pathlib import Path
import sys
if os.environ.get("FAKE_GO_FAIL"):
    sys.exit(1)
for p in ["cmd/kueuectl", "pkg/shared"]:
    print(Path.cwd() / p)
if os.environ["GOOS"] == "linux":
    print(Path.cwd() / "pkg/linux")
''')
        self.go.chmod(0o755)
        self.make = self.directory / "make"
        self.make.write_text('''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
with open(os.environ["MAKE_LOG"], "a") as f:
    f.write(json.dumps(sys.argv[1:]) + "\\n")
if os.environ.get("FAKE_FAIL_TARGET") in sys.argv[1:]:
    sys.exit(7)
if "verify-tree-prereqs" in sys.argv[1:] and os.environ.get("FAKE_GENERATE"):
    Path("site/generated.md").write_text("regenerated\\n")
''')
        self.make.chmod(0o755)
        self.log = self.directory / "make.jsonl"
        self.env = dict(os.environ, MAKE_LOG=str(self.log))
        self.env.pop("FAKE_GO_FAIL", None)
        self.env.pop("FAKE_GENERATE", None)
        self.env.pop("FAKE_FAIL_TARGET", None)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], stderr=subprocess.DEVNULL)

    def write(self, name, contents):
        p = self.root / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(contents)

    def run_selector(self, plan=True, base="main"):
        command = [sys.executable, str(SELECTOR), "--base-ref", base, "--go", str(self.go), "--make", str(self.make)]
        if plan:
            command.append("--plan")
        command += ["--paths", "apis", "site", "client-go"]
        return subprocess.run(command, cwd=self.root, env=self.env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_no_changes_runs_nothing(self):
        result = self.run_selector(plan=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls(), [])

    def test_docs_do_not_load_go_or_build_cli(self):
        self.write("docs.md", "edited\n")
        self.go.unlink()
        result = self.run_selector()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Generators: verify-docs", result.stdout)
        self.assertIn("Checks: (none)", result.stdout)
        self.assertNotIn("Could not resolve", result.stderr)

    def test_shared_cli_dependency_builds_cli(self):
        self.write("pkg/shared/shared.go", "edited\n")
        result = self.run_selector()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("verify-cli", result.stdout)
        self.assertIn("generate-kueuectl-docs", result.stdout)

    def test_linux_only_dependency_is_detected_on_any_host(self):
        self.write("pkg/linux/client.go", "edited\n")
        result = self.run_selector()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("verify-cli", result.stdout)

    def test_unrelated_go_does_not_build_cli(self):
        self.write("pkg/scheduler/scheduler.go", "edited\n")
        result = self.run_selector(plan=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls(), [["-j", "8", "verify-go"]])
        self.assertEqual((self.root / "pkg/scheduler/scheduler.go").read_text(), "edited\n")

    def test_test_only_change_does_not_regenerate_or_build_cli(self):
        self.write("apis/config/v1beta2/defaults_test.go", "edited\n")
        result = self.run_selector()
        self.assertIn("Generators: (none)", result.stdout)
        self.assertIn("Checks: verify-go", result.stdout)

    def test_committed_changes_are_not_lost_after_push(self):
        self.write("pkg/shared/shared.go", "edited\n")
        self.git("add", ".")
        self.git("commit", "-m", "Update shared code")
        result = self.run_selector()
        self.assertIn("Changed files: 1", result.stdout)
        self.assertIn("verify-cli", result.stdout)

    def test_staged_unstaged_and_untracked_changes_are_combined(self):
        self.write("pkg/shared/shared.go", "edited\n")
        self.git("add", "pkg/shared/shared.go")
        self.write("docs.md", "edited\n")
        self.write("cmd/experimental/skills/example/SKILL.md", "new skill\n")
        result = self.run_selector()
        self.assertIn("Changed files: 3", result.stdout)
        for target in ("verify-cli", "verify-docs", "verify-skills-lint"):
            self.assertIn(target, result.stdout)

    def test_deleted_and_renamed_files_select_both_sides(self):
        self.git("mv", "pkg/shared/shared.go", "pkg/scheduler/moved.go")
        (self.root / "hack/testing/e2e-common.sh").unlink()
        result = self.run_selector()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Changed files: 3", result.stdout)
        self.assertIn("verify-cli", result.stdout)
        self.assertIn("shell-lint", result.stdout)
        self.assertIn("e2e-common-test", result.stdout)

    def test_module_build_or_unknown_changes_fall_back(self):
        for path in ("go.mod", "Makefile", "hack/tools/code-generator/generate.sh", "hack/tools/compatibility-lifecycle/generate.sh", "hack/helm-chart-package.sh", "new-unclassified-input.yaml"):
            with self.subTest(path=path):
                self.write(path, "edited\n")
                result = self.run_selector()
                self.assertIn("Checks: verify-checks", result.stdout)
                self.assertIn("Full verification checks selected", result.stdout)
                self.git("reset", "--hard", "HEAD")
                self.git("clean", "-fd")

    def test_manifest_sources_generate_before_checks(self):
        for path in ("pkg/controller/core/resourceflavor_controller.go", "pkg/webhooks/workload_webhook.go", "pkg/util/cert/cert.go", "pkg/visibility/api.go", "pkg/config/config.go"):
            with self.subTest(path=path):
                self.write(path, "// +kubebuilder:rbac:groups=test\n")
                self.log.unlink(missing_ok=True)
                result = self.run_selector(plan=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("verify-manifests", self.calls()[0])
                self.assertNotIn("verify-helm", self.calls()[0])
                self.assertIn("verify-helm", self.calls()[1])
                self.assertNotIn("verify-manifests", self.calls()[1])
                if path.startswith("pkg/controller/core/"):
                    self.assertIn("generate-mocks", self.calls()[0])
                self.git("clean", "-fd")

    def test_dependency_resolution_failure_includes_cli(self):
        self.write("pkg/scheduler/scheduler.go", "edited\n")
        self.env["FAKE_GO_FAIL"] = "1"
        result = self.run_selector()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("verify-cli", result.stdout)
        self.assertIn("conservatively", result.stderr)

    def test_invalid_base_fails_without_running_make(self):
        result = self.run_selector(plan=False, base="missing-base")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls(), [])

    def test_generator_failure_stops_checks(self):
        self.write("apis/config/v1beta2/defaults.go", "edited\n")
        self.env["FAKE_FAIL_TARGET"] = "verify-tree-prereqs"
        result = self.run_selector(plan=False)
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertEqual(len(self.calls()), 1)
        self.assertIn("verify-tree-prereqs", self.calls()[0])

    def test_check_failure_is_propagated(self):
        self.write("pkg/shared/shared.go", "edited\n")
        self.env["FAKE_FAIL_TARGET"] = "verify-cli"
        result = self.run_selector(plan=False)
        self.assertEqual(result.returncode, 7, result.stderr)

    def test_regenerated_outputs_are_reported_and_preserved(self):
        self.write("apis/config/v1beta2/defaults.go", "edited\n")
        self.env["FAKE_GENERATE"] = "1"
        result = self.run_selector(plan=False)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("site/generated.md", result.stderr)
        self.assertEqual((self.root / "site/generated.md").read_text(), "regenerated\n")
        self.assertIn("verify-tree-prereqs", self.calls()[0])
        self.assertNotIn("verify-go", self.calls()[0])
        self.assertIn("verify-go", self.calls()[1])


if __name__ == "__main__":
    unittest.main()
