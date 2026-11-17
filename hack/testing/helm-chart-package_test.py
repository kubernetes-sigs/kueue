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

"""Exercise push_chart from hack/helm-chart-package.sh with a fake helm."""

import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "hack" / "helm-chart-package.sh"
DIGEST = "sha256:" + "ab" * 32

FAKE_HELM = """#!/bin/bash
case "$FAKE_HELM_MODE" in
  digest) echo "Pushed: example.test/charts/kueue:v1"; echo "Digest: __DIGEST__" ;;
  nodigest) echo "Pushed: example.test/charts/kueue:v1" ;;
  fail) echo "Error: boom" >&2; exit 1 ;;
esac
""".replace("__DIGEST__", DIGEST)


def push_chart_source():
    """Return the push_chart function exactly as it appears in the shipped script."""
    lines = SCRIPT.read_text().splitlines()
    start = lines.index("push_chart() {")
    end = lines.index("}", start)
    return "\n".join(lines[start:end + 1])


class HelmChartPackageTest(unittest.TestCase):
    def run_push_chart(self, mode, digest_dir=None):
        directory = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, directory, True)
        helm = directory / "helm"
        helm.write_text(FAKE_HELM)
        helm.chmod(0o755)
        driver = directory / "driver.sh"
        driver.write_text(
            "set -o errexit -o nounset -o pipefail\n"
            f"HELM={shlex.quote(str(helm))}\n"
            "HELM_CHART_REPO=example.test/charts\n"
            f"{push_chart_source()}\n"
            "push_chart charts/kueue-0.1.0.tgz kueue\n"
        )
        env = {**os.environ, "FAKE_HELM_MODE": mode}
        env.pop("DIGEST_DIR", None)
        if digest_dir is not None:
            env["DIGEST_DIR"] = str(digest_dir)
        result = subprocess.run(["bash", str(driver)], env=env, text=True, capture_output=True)
        return result, directory

    def test_digest_line_is_written_to_the_chart_digest_file(self):
        """Record the digest that helm push prints, and keep the push output in the log."""
        digest_dir = Path(tempfile.mkdtemp()) / "digests"
        self.addCleanup(shutil.rmtree, digest_dir.parent, True)
        result, _ = self.run_push_chart("digest", digest_dir)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("Pushed: example.test/charts/kueue:v1", result.stdout)
        self.assertEqual((digest_dir / "chart-kueue.digest").read_text(), DIGEST + "\n")

    def test_missing_digest_line_fails_without_a_digest_file(self):
        digest_dir = Path(tempfile.mkdtemp()) / "digests"
        self.addCleanup(shutil.rmtree, digest_dir.parent, True)
        result, _ = self.run_push_chart("nodigest", digest_dir)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("printed no digest", result.stderr)
        self.assertFalse((digest_dir / "chart-kueue.digest").exists())

    def test_helm_push_failure_fails_without_a_digest_file(self):
        digest_dir = Path(tempfile.mkdtemp()) / "digests"
        self.addCleanup(shutil.rmtree, digest_dir.parent, True)
        result, _ = self.run_push_chart("fail", digest_dir)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Error: boom", result.stderr)
        self.assertFalse((digest_dir / "chart-kueue.digest").exists())

    def test_unset_digest_dir_pushes_without_recording(self):
        """Keep the plain helm push when DIGEST_DIR is unset."""
        result, directory = self.run_push_chart("digest", None)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(sorted(p.name for p in directory.iterdir()), ["driver.sh", "helm"])


if __name__ == "__main__":
    unittest.main()
