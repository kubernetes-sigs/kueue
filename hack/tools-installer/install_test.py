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

"""Exercise installer reuse, release formats, and failures without network access."""

import hashlib
import importlib.util
import io
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import call, patch
from urllib.error import HTTPError, URLError

SPEC = importlib.util.spec_from_file_location("installer", Path(__file__).with_name("install.py"))
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)


class InstallTest(unittest.TestCase):
    def test_reuse_and_upgrade(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(installer.subprocess, "check_output", return_value="linux\namd64\n"):
            binary = Path(temp) / "kind"
            with patch.object(installer, "release_binary", return_value=b"binary") as download:
                installer.install("kind", "v1", binary)
                installer.install("kind", "v1", binary)
                self.assertEqual(download.call_count, 1)
                installer.install("kind", "v2", binary)
                self.assertEqual(download.call_count, 2)
                with patch.object(installer.subprocess, "check_output", return_value="darwin\narm64\n"):
                    installer.install("kind", "v2", binary)
                    installer.install("kind", "v2", binary)
                self.assertEqual(download.call_count, 3)
                binary.unlink()
                installer.install("kind", "v2", binary)
                self.assertEqual(download.call_count, 4)

    def test_failed_upgrade_preserves_installed_version(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(installer.subprocess, "check_output", return_value="linux\namd64\n"):
            binary = Path(temp) / "kind"
            binary.write_bytes(b"old")
            with patch.object(installer, "release_binary", side_effect=ValueError("SHA256 mismatch")), patch.object(installer, "install_source") as source:
                with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                    installer.install("kind", "v2", binary)
                source.assert_not_called()
            self.assertEqual(binary.read_bytes(), b"old")
            self.assertFalse(binary.with_name("kind.release-version").exists())

    def test_kind_uses_per_asset_checksum_without_api(self):
        content = b"release binary"
        checksum = hashlib.sha256(content).hexdigest().encode()
        with patch.object(installer, "download", side_effect=[checksum, content]) as download:
            self.assertEqual(installer.release_binary("kind", "v1", "linux", "amd64"), content)
            self.assertEqual(download.call_args_list, [
                call("https://github.com/kubernetes-sigs/kind/releases/download/v1/kind-linux-amd64.sha256sum"),
                call("https://github.com/kubernetes-sigs/kind/releases/download/v1/kind-linux-amd64"),
            ])

    def test_corrupt_binary_is_rejected(self):
        with patch.object(installer, "download", side_effect=[hashlib.sha256(b"correct").hexdigest().encode(), b"corrupt"]):
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                installer.release_binary("kind", "v1", "linux", "amd64")

    def test_older_release_checksums(self):
        content = b"old binary"
        for checksum in (
            hashlib.sha256(content).hexdigest() + "  mdtoc-amd64-linux",
            "SHA256 (mdtoc-amd64-linux) = " + hashlib.sha256(content).hexdigest(),
        ):
            with self.subTest(checksum=checksum):
                with patch.object(installer, "download", side_effect=[checksum.encode(), content]):
                    self.assertEqual(installer.release_binary("mdtoc", "v1", "linux", "amd64"), content)

    def test_unavailable_release_binary_falls_back_to_source(self):
        checksum = f"{'a' * 64}  mdtoc-amd64-linux\n".encode()
        missing = HTTPError("https://github.com/example/release", 404, "not found", {}, None)
        other_host = f"{'a' * 64}  mdtoc-arm64-linux\n".encode()
        for responses in ([missing], [checksum, missing], [other_host]):
            with self.subTest(responses=responses), tempfile.TemporaryDirectory() as temp, patch.object(installer.subprocess, "check_output", return_value="linux\namd64\n"):
                binary = Path(temp) / "mdtoc"
                with patch.object(installer, "download", side_effect=responses), patch.object(installer, "install_source") as source:
                    installer.install("mdtoc", "v1", binary)
                    source.assert_called_once_with("mdtoc", "v1", binary)
                self.assertEqual(binary.with_name("mdtoc.release-version").read_text(), "v1 linux/amd64\nsource\n")

    def test_install_errors_do_not_fall_back_to_source(self):
        checksum = f"{hashlib.sha256(b'correct').hexdigest()}  mdtoc-amd64-linux\n".encode()
        cases = {
            "checksum mismatch": ([checksum, b"corrupt"], ValueError),
            "invalid manifest": ([b"invalid checksum"], ValueError),
            "forbidden": ([HTTPError("url", 403, "forbidden", {}, None)], HTTPError),
            "unavailable": ([HTTPError("url", 503, "unavailable", {}, None)], HTTPError),
        }
        for name, (responses, error) in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp, patch.object(installer.subprocess, "check_output", return_value="linux\namd64\n"):
                binary = Path(temp) / "mdtoc"
                with patch.object(installer, "download", side_effect=responses), patch.object(installer, "install_source") as source:
                    with self.assertRaises(error):
                        installer.install("mdtoc", "v1", binary)
                    source.assert_not_called()
                self.assertFalse(binary.with_name("mdtoc.release-version").exists())

    def test_unsupported_host_uses_cached_source_without_repeating_download(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(installer.subprocess, "check_output", return_value="linux\ns390x\n"):
            binary = Path(temp) / "helm-docs"

            def source(*args):
                binary.write_bytes(b"host binary")
                binary.chmod(0o755)

            with patch.object(installer, "release_binary", side_effect=installer.NoReleaseBinary("no host asset")) as download, patch.object(installer, "install_source", side_effect=source) as compile_source:
                installer.install("helm-docs", "v1", binary)
                installer.install("helm-docs", "v1", binary)
                self.assertEqual(download.call_count, 1)
                self.assertEqual(compile_source.call_count, 2)
                compile_source.assert_called_with("helm-docs", "v1", binary)

    def test_source_fallback_uses_pinned_package(self):
        """Check every tool's fallback module/package and forward the pinned version.

        Keep these expectations independent of data.json so an incorrect package
        mapping cannot silently build the wrong executable on unsupported hosts.
        Mock the subprocess to check the source install command without compiling.
        """
        packages = {
            "golangci-lint": ("github.com/golangci/golangci-lint/v2", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"),
            "kustomize": ("sigs.k8s.io/kustomize/kustomize/v5", "sigs.k8s.io/kustomize/kustomize/v5"),
            "gotestsum": ("gotest.tools/gotestsum", "gotest.tools/gotestsum"),
            "kind": ("sigs.k8s.io/kind", "sigs.k8s.io/kind"),
            "yq": ("github.com/mikefarah/yq/v4", "github.com/mikefarah/yq/v4"),
            "mdtoc": ("sigs.k8s.io/mdtoc", "sigs.k8s.io/mdtoc"),
            "helm-docs": ("github.com/norwoodj/helm-docs", "github.com/norwoodj/helm-docs/cmd/helm-docs"),
        }
        for name, (module, package) in packages.items():
            with self.subTest(name=name), patch.object(installer.subprocess, "run") as run:
                installer.install_source(name, "v1", f"/tmp/{name}")
                self.assertEqual(run.call_args.args[0], ["bash", str(Path(__file__).with_name("source.sh")), f"/tmp/{name}", module, "v1", package])

    def test_source_build_failure_logs_compiler_output(self):
        result = subprocess.CompletedProcess(["go", "install"], 1, stdout="compiler failure details\n")
        with patch.object(installer.subprocess, "run", return_value=result), self.assertLogs(installer.LOG, level="DEBUG") as logs:
            with self.assertRaises(subprocess.CalledProcessError):
                installer.install_source("helm-docs", "v1", "/tmp/helm-docs")
            self.assertIn("compiler failure details", "\n".join(logs.output))

    def test_archive_uses_only_binary_contents(self):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            binary = tarfile.TarInfo("../../outside/kustomize")
            binary.size = 4
            archive.addfile(binary, io.BytesIO(b"tool"))
        content = buffer.getvalue()
        asset = {"name": "kustomize_v1_linux_amd64.tar.gz", "browser_download_url": "https://example.test/archive", "digest": "sha256:" + hashlib.sha256(content).hexdigest()}
        manifest = (hashlib.sha256(content).hexdigest() + "  " + asset["name"]).encode()
        with patch.object(installer, "download", side_effect=[manifest, content]):
            self.assertEqual(installer.release_binary("kustomize", "v1", "linux", "amd64"), b"tool")

    def test_checksum_manifest_avoids_api_requests(self):
        for name in ("golangci-lint", "kustomize", "gotestsum", "yq", "mdtoc", "helm-docs"):
            with self.subTest(name=name):
                repo, tag, filename = installer.release(name, "v1", "linux", "amd64")
                content = b"binary"
                if filename.endswith(".tar.gz"):
                    buffer = io.BytesIO()
                    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
                        binary = tarfile.TarInfo(name)
                        binary.size = len(content)
                        archive.addfile(binary, io.BytesIO(content))
                    content = buffer.getvalue()
                manifest = f"SHA256 ({filename}) = {hashlib.sha256(content).hexdigest()}\n".encode()
                with patch.object(installer, "download", side_effect=[manifest, content]) as download:
                    self.assertEqual(installer.release_binary(name, "v1", "linux", "amd64"), b"binary")
                    self.assertEqual(download.call_count, 2)
                    for request in download.call_args_list:
                        self.assertTrue(request.args[0].startswith(f"https://github.com/{repo}/releases/download/"))

    def test_invalid_checksum_manifest_is_not_a_missing_binary(self):
        with patch.object(installer, "download", return_value=b"unrecognized format"):
            with self.assertRaisesRegex(ValueError, "No SHA256 checksums") as error:
                installer.release_binary("mdtoc", "v1", "linux", "amd64")
            self.assertNotIsInstance(error.exception, installer.NoReleaseBinary)

    def test_manifest_without_host_asset_allows_source_build(self):
        manifest = f"{'a' * 64}  mdtoc-arm64-linux\n".encode()
        with patch.object(installer, "download", return_value=manifest) as download:
            with self.assertRaises(installer.NoReleaseBinary):
                installer.release_binary("mdtoc", "v1", "linux", "s390x")
            download.assert_called_once_with("https://github.com/kubernetes-sigs/mdtoc/releases/download/v1/checksums.txt")

    def test_transient_failure_exhausts_seven_attempts(self):
        for error in (HTTPError("url", 408, "request timeout", {}, None), HTTPError("url", 503, "unavailable", {}, None), URLError("timeout"), TimeoutError("timed out"), installer.HTTPException("incomplete response"), ConnectionError("reset")):
            with self.subTest(error=error), patch.object(installer, "urlopen", side_effect=error) as request, patch.object(installer.time, "sleep") as sleep, self.assertLogs(installer.LOG, level="DEBUG") as logs:
                with self.assertRaises(type(error)):
                    installer.download("https://example.test/file")
                self.assertEqual(request.call_count, 7)
                self.assertEqual(sleep.call_args_list, [call(delay) for delay in (2, 4, 8, 16, 32, 64)])
                self.assertEqual(sum("Download failed" in line for line in logs.output), 7)
                self.assertIn(str(error), "\n".join(logs.output))

    def test_rate_limit_uses_fixed_wait(self):
        with patch.object(installer, "urlopen", side_effect=HTTPError("url", 429, "limit", {}, None)) as request, patch.object(installer.time, "sleep") as sleep, self.assertLogs(installer.LOG):
            with self.assertRaises(HTTPError):
                installer.download("https://example.test/file")
            self.assertEqual(request.call_count, 7)
            self.assertEqual(sleep.call_args_list, [call(20)] * 6)

    def test_download_succeeds_on_last_attempt(self):
        failures = [URLError("timeout")] * 6
        with patch.object(installer, "urlopen", side_effect=[*failures, io.BytesIO(b"binary")]) as request, patch.object(installer.time, "sleep") as sleep, self.assertLogs(installer.LOG, level="DEBUG") as logs:
            self.assertEqual(installer.download("https://example.test/file"), b"binary")
            self.assertEqual(request.call_count, 7)
            self.assertEqual(sleep.call_args_list, [call(delay) for delay in (2, 4, 8, 16, 32, 64)])
            self.assertIn("Downloaded 6 bytes", "\n".join(logs.output))

    def test_interrupted_response_is_retried(self):
        response = unittest.mock.MagicMock()
        response.__enter__.return_value.read.side_effect = installer.HTTPException("interrupted read")
        with patch.object(installer, "urlopen", side_effect=[response, io.BytesIO(b"binary")]), patch.object(installer.time, "sleep") as sleep, self.assertLogs(installer.LOG):
            self.assertEqual(installer.download("https://example.test/file"), b"binary")
            sleep.assert_called_once_with(2)

    def test_missing_release_is_not_retried(self):
        for code in (400, 401, 403, 404):
            with self.subTest(code=code), patch.object(installer, "urlopen", side_effect=HTTPError("url", code, "missing", {}, None)) as request, patch.object(installer.time, "sleep") as sleep, self.assertLogs(installer.LOG):
                with self.assertRaises(HTTPError):
                    installer.download("https://example.test/file")
                self.assertEqual(request.call_count, 1)
                sleep.assert_not_called()

    def test_cli_logs_are_separate_from_release_artifacts(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fake_go = root / "go"
            fake_go.write_text("#!/bin/sh\nprintf 'linux\\namd64\\n'\n")
            fake_go.chmod(0o755)
            binary = root / "kind"
            binary.write_bytes(b"binary")
            binary.chmod(0o755)
            (root / "kind.release-version").write_text("v1 linux/amd64\n")
            artifacts = root / "release-artifacts"
            logs = root / "installation-logs"
            env = {**os.environ, "GO_CMD": str(fake_go), "ARTIFACTS": str(artifacts), "INSTALL_LOG_DIR": str(logs)}
            env.pop("PROW_JOB_ID", None)
            script = str(Path(__file__).resolve().with_name("install.py"))
            for name, status, message in [("kind", 0, "Reusing kind"), ("unknown", 1, "No upstream binary configured")]:
                # Resolve the adjacent JSON independently of the caller's directory.
                result = subprocess.run([sys.executable, script, name, "v1", str(root / name)], cwd=root, env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, status, result.stderr)
                log = (logs / f"install-{name}.log").read_text()
                self.assertIn(message, log)
                self.assertIn(message, result.stderr)
                self.assertIn("DEBUG", log)
                self.assertNotIn("DEBUG", result.stderr)
                if status:
                    self.assertIn("Traceback", log)
            self.assertFalse(artifacts.exists())
            (logs / "install-kind.log").write_text("obsolete previous invocation\n")
            subprocess.run([sys.executable, script, "kind", "v1", str(binary)], cwd=root, env=env, capture_output=True, text=True, check=True)
            self.assertNotIn("obsolete previous invocation", (logs / "install-kind.log").read_text())

    def test_prow_logs_can_be_collected_again_after_artifact_cleanup(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fake_go = root / "go"
            fake_go.write_text("#!/bin/sh\nprintf 'linux\\namd64\\n'\n")
            fake_go.chmod(0o755)
            binary = root / "kind"
            binary.write_bytes(b"binary")
            binary.chmod(0o755)
            (root / "kind.release-version").write_text("v1 linux/amd64\n")
            artifacts = root / "artifacts"
            logs = root / "installation-logs"
            env = {**os.environ, "GO_CMD": str(fake_go), "ARTIFACTS": str(artifacts), "INSTALL_LOG_DIR": str(logs), "PROW_JOB_ID": "test-prow-job"}
            script = str(Path(__file__).resolve().with_name("install.py"))
            subprocess.run([sys.executable, script, "kind", "v1", str(binary)], env=env, capture_output=True, text=True, check=True)
            archived = artifacts / "tool-installation" / "install-kind.log"
            original = (logs / "install-kind.log").read_text()
            self.assertIn("DEBUG", original)
            self.assertEqual(archived.read_text(), original)
            archived.unlink()
            archived.parent.rmdir()
            (logs / "unrelated.txt").write_text("keep outside CI artifacts\n")
            env["GO_CMD"] = str(root / "missing-go")
            # Collection must survive cleanup without invoking Go or replacing the source log.
            subprocess.run([sys.executable, script, "--collect-logs"], env=env, capture_output=True, text=True, check=True)
            subprocess.run([sys.executable, script, "--collect-logs"], env=env, capture_output=True, text=True, check=True)
            self.assertEqual(archived.read_text(), original)
            self.assertEqual((logs / "install-kind.log").read_text(), original)
            self.assertEqual(list(archived.parent.iterdir()), [archived])

    def test_collection_does_not_write_release_artifacts_outside_prow(self):
        for settings in ({"ARTIFACTS": "release-artifacts"}, {"PROW_JOB_ID": "test-prow-job"}):
            with self.subTest(settings=settings), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                logs = root / "logs"
                logs.mkdir()
                (logs / "install-kind.log").write_text("diagnostics\n")
                env = {**os.environ, "INSTALL_LOG_DIR": str(logs), "GO_CMD": str(root / "missing-go")}
                env.pop("PROW_JOB_ID", None)
                env.pop("ARTIFACTS", None)
                env.update({name: str(root / value) if name == "ARTIFACTS" else value for name, value in settings.items()})
                script = str(Path(__file__).resolve().with_name("install.py"))
                subprocess.run([sys.executable, script, "--collect-logs"], cwd=root, env=env, capture_output=True, text=True, check=True)
                self.assertEqual(list(root.iterdir()), [logs])

    def test_collection_failure_does_not_hide_installation_result(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            logs = root / "logs"
            logs.mkdir()
            (logs / "install-kind.log").write_text("installation diagnostics\n")
            settings = {"INSTALL_LOG_DIR": str(logs), "ARTIFACTS": str(root / "artifacts"), "PROW_JOB_ID": "test-prow-job"}
            with patch.dict(os.environ, settings), patch.object(installer.shutil, "copyfile", side_effect=OSError("artifact storage unavailable")), self.assertLogs(installer.LOG, level="WARNING") as captured:
                installer.collect_logs()
            self.assertIn("artifact storage unavailable", "\n".join(captured.output))
            self.assertEqual((logs / "install-kind.log").read_text(), "installation diagnostics\n")

    def test_verify_failure_collects_logs_after_artifact_cleanup(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            repository = Path(__file__).resolve().parents[2]
            tool_dir = root / "hack/tools-installer"
            tool_dir.mkdir(parents=True)
            for name in ("install.py", "data.json"):
                shutil.copyfile(Path(__file__).with_name(name), tool_dir / name)
            logs = root / "logs"
            archived = root / "artifacts/tool-installation/install-kind.log"
            archived.parent.mkdir(parents=True)
            archived.write_text("obsolete diagnostics\n")
            summary = root / "hack/make-failure-summary.sh"
            summary.write_text('''#!/bin/sh
mkdir -p "$INSTALL_LOG_DIR"
printf 'failed verification diagnostics\\n' > "$INSTALL_LOG_DIR/install-kind.log"
rm "$ARTIFACTS/tool-installation/install-kind.log"
rmdir "$ARTIFACTS/tool-installation"
exit 1
''')
            summary.chmod(0o755)
            fake_git = root / "git"
            fake_git.write_text('#!/bin/sh\ntouch "$VERIFY_TEST_GIT_CALLED"\nexit 0\n')
            fake_git.chmod(0o755)
            (root / "Makefile").write_text(
                f"PROJECT_DIR := {root}\nTESTING_DIR := {repository / 'hack/testing'}\n"
                f"CONTAINER_ENGINE := true\nVERIFY_OUTPUT_SYNC :=\ninclude {repository / 'hack/make/verify.mk'}\n"
            )
            git_called = root / "git-called"
            env = {
                **os.environ,
                "INSTALL_LOG_DIR": str(logs),
                "ARTIFACTS": str(archived.parent.parent),
                "PROW_JOB_ID": "test-prow-job",
                "VERIFY_TEST_GIT_CALLED": str(git_called),
                "PATH": f"{root}{os.pathsep}{os.environ['PATH']}",
            }
            result = subprocess.run(["make", "verify"], cwd=root, env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(git_called.exists())
            self.assertEqual(archived.read_text(), "failed verification diagnostics\n")

    def test_default_logs_are_relative_to_repository_root(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            script = root / "hack/tools-installer/install.py"
            with patch.object(installer, "__file__", str(script)), patch.dict(os.environ, {}, clear=True), patch.object(installer.logging, "basicConfig") as configure:
                installer.configure_logging("kind")
                try:
                    self.assertTrue((root / ".cache/tool-installation/install-kind.log").is_file())
                finally:
                    for handler in configure.call_args.kwargs["handlers"]:
                        handler.close()

    def test_default_logs_are_ignored_by_git(self):
        with patch.dict(os.environ, {}, clear=True):
            log = installer.log_directory() / "install-kind.log"
        result = subprocess.run(["git", "check-ignore", "--quiet", str(log)], cwd=Path(__file__).parent, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_supported_release_names(self):
        expected = {
            "golangci-lint": ("golangci/golangci-lint", "v1.2.3", "golangci-lint-1.2.3-darwin-arm64.tar.gz"),
            "kustomize": ("kubernetes-sigs/kustomize", "kustomize/v1.2.3", "kustomize_v1.2.3_darwin_arm64.tar.gz"),
            "gotestsum": ("gotestyourself/gotestsum", "v1.2.3", "gotestsum_1.2.3_darwin_arm64.tar.gz"),
            "kind": ("kubernetes-sigs/kind", "v1.2.3", "kind-darwin-arm64"),
            "yq": ("mikefarah/yq", "v1.2.3", "yq_darwin_arm64"),
            "mdtoc": ("kubernetes-sigs/mdtoc", "v1.2.3", "mdtoc-arm64-darwin"),
            "helm-docs": ("norwoodj/helm-docs", "v1.2.3", "helm-docs_1.2.3_Darwin_arm64.tar.gz"),
        }
        for name, want in expected.items():
            with self.subTest(name=name):
                self.assertEqual(installer.release(name, "v1.2.3", "darwin", "arm64"), want)
        self.assertEqual(installer.release("helm-docs", "v1", "linux", "amd64")[2], "helm-docs_1_Linux_x86_64.tar.gz")
        with self.assertRaisesRegex(ValueError, "No upstream binary"):
            installer.release("unknown", "v1", "linux", "amd64")

    def test_tool_registry_drives_release_and_source_installation(self):
        tool = {
            "module": "example.test/tools",
            "package": "example.test/tools/cmd/example-tool",
            "repository": "example/tools",
            "tag": "tool/{version}",
            "asset": "example-tool_{version}_{goos}_{goarch}",
            "checksum": "checksums-{number}.txt",
        }
        with patch.dict(installer.TOOLS, {"example-tool": tool}):
            repo, tag, asset = installer.release("example-tool", "v2.3.4", "linux", "amd64")
            self.assertEqual((repo, tag, asset), ("example/tools", "tool/v2.3.4", "example-tool_v2.3.4_linux_amd64"))
            content = b"custom binary"
            manifest = f"{hashlib.sha256(content).hexdigest()}  {asset}".encode()
            with patch.object(installer, "download", side_effect=[manifest, content]) as download:
                self.assertEqual(installer.release_binary("example-tool", "v2.3.4", "linux", "amd64"), content)
                self.assertEqual(download.call_args_list, [
                    call("https://github.com/example/tools/releases/download/tool%2Fv2.3.4/checksums-2.3.4.txt"),
                    call("https://github.com/example/tools/releases/download/tool%2Fv2.3.4/example-tool_v2.3.4_linux_amd64"),
                ])
            with patch.object(installer.subprocess, "run") as run:
                installer.install_source("example-tool", "v2.3.4", "/tmp/example-tool")
                self.assertEqual(run.call_args.args[0][-3:], ["example.test/tools", "v2.3.4", "example.test/tools/cmd/example-tool"])

    def test_source_tool_rebuilds_only_when_required(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            fake_go = directory / "go"
            fake_go.write_text('''#!/usr/bin/env python3
import os, pathlib, sys
if sys.argv[1] == "env":
    values = {"GOVERSION": os.environ.get("SOURCE_TEST_VERSION", "go1.27.0"), "GOHOSTOS": "linux", "GOHOSTARCH": os.environ.get("SOURCE_TEST_ARCH", "amd64"), "GOFLAGS": os.environ.get("GOFLAGS", "")}
    print("\\n".join(values[key] for key in sys.argv[2:]))
else:
    assert os.environ["GOOS"] == "linux" and os.environ["GOARCH"] == os.environ.get("SOURCE_TEST_ARCH", "amd64")
    binary = pathlib.Path(os.environ["GOBIN"]) / "generator"
    with binary.with_suffix(".calls").open("a") as calls:
        calls.write(sys.argv[-1] + "\\n")
    binary.write_text("binary")
    binary.chmod(0o755)
''')
            fake_go.chmod(0o755)
            env = {**os.environ, "GO_CMD": str(fake_go), "GOOS": "windows", "GOARCH": "386", "GOFLAGS": "", "CGO_ENABLED": "1"}
            command = ["bash", str(Path(__file__).with_name("source.sh")), str(directory / "generator"), "example/generator", "v1", "example/generator"]
            subprocess.run(command, env=env, check=True)
            subprocess.run(command, env=env, check=True)
            command[-2] = "v2"
            subprocess.run(command, env=env, check=True)
            env["GOFLAGS"] = "-tags=custom"
            subprocess.run(command, env=env, check=True)
            subprocess.run(command, env=env, check=True)
            command[-1] = "example/generator/cmd/generator"
            subprocess.run(command, env=env, check=True)
            env["SOURCE_TEST_VERSION"] = "go1.27.1"
            subprocess.run(command, env=env, check=True)
            env["SOURCE_TEST_ARCH"] = "arm64"
            subprocess.run(command, env=env, check=True)
            env["CGO_ENABLED"] = "0"
            subprocess.run(command, env=env, check=True)
            subprocess.run(command, env=env, check=True)
            self.assertEqual((directory / "generator.calls").read_text().splitlines(), [
                "example/generator@v1",
                "example/generator@v2",
                "example/generator@v2",
                *["example/generator/cmd/generator@v2"] * 4,
            ])

    def test_source_build_retries_without_marking_failed_installations(self):
        for failures in (1, 7):
            with self.subTest(failures=failures), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                fake_go = root / "go"
                fake_go.write_text('''#!/usr/bin/env python3
import os, pathlib, sys
if sys.argv[1] == "env":
    values = {"GOVERSION": "go1.27.0", "GOHOSTOS": "linux", "GOHOSTARCH": "amd64", "GOFLAGS": ""}
    print("\\n".join(values[key] for key in sys.argv[2:]))
else:
    binary = pathlib.Path(os.environ["GOBIN"]) / "generator"
    calls = binary.with_suffix(".calls")
    count = int(calls.read_text()) + 1 if calls.exists() else 1
    calls.write_text(str(count))
    if count <= int(os.environ["SOURCE_TEST_FAILURES"]):
        print("temporary source download failure", file=sys.stderr)
        sys.exit(1)
    binary.write_text("binary")
    binary.chmod(0o755)
''')
                fake_go.chmod(0o755)
                sleep = root / "sleep"
                sleep.write_text("#!/bin/sh\nexit 0\n")
                sleep.chmod(0o755)
                env = {**os.environ, "GO_CMD": str(fake_go), "SOURCE_TEST_FAILURES": str(failures), "PATH": f"{root}{os.pathsep}{os.environ['PATH']}"}
                binary = root / "generator"
                command = ["bash", str(Path(__file__).with_name("source.sh")), str(binary), "example/generator", "v1", "example/generator"]
                result = subprocess.run(command, env=env, capture_output=True, text=True)
                self.assertIn("temporary source download failure", result.stderr)
                self.assertEqual(int((root / "generator.calls").read_text()), min(failures + 1, 7))
                marker = root / "generator.source-version"
                if failures < 7:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertTrue(marker.is_file())
                    subprocess.run(command, env=env, capture_output=True, text=True, check=True)
                    self.assertEqual(int((root / "generator.calls").read_text()), failures + 1)
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(marker.exists())
                    self.assertFalse(binary.exists())

    def test_codegen_downloads_module_before_querying_source_directory(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            script = root / "hack/tools/code-generator/generate.sh"
            script.parent.mkdir(parents=True)
            shutil.copyfile(Path(__file__).resolve().parents[1] / "tools/code-generator/generate.sh", script)
            retry = root / "hack/testing/retry.sh"
            retry.parent.mkdir()
            shutil.copyfile(Path(__file__).resolve().parents[1] / "testing/retry.sh", retry)
            module = root / "module"
            module.mkdir()
            (module / "kube_codegen.sh").write_text("\n".join(f"kube::codegen::{name}() {{ :; }}" for name in ["gen_helpers", "gen_openapi", "gen_client"]))
            fake_go = root / "go"
            fake_go.write_text('''#!/usr/bin/env python3
import os, pathlib, sys
root = pathlib.Path(os.environ["CODEGEN_TEST_ROOT"])
if sys.argv[1:] == ["mod", "download", "k8s.io/code-generator"]:
    calls = root / "download-calls"
    count = int(calls.read_text()) + 1 if calls.exists() else 1
    calls.write_text(str(count))
    if count == 1:
        print("temporary module download failure", file=sys.stderr)
        sys.exit(1)
    (root / "downloaded").touch()
elif sys.argv[1] == "list" and (root / "downloaded").exists():
    print(root / "module")
else:
    sys.exit(1)
''')
            fake_go.chmod(0o755)
            sleep = root / "sleep"
            sleep.write_text("#!/bin/sh\nexit 0\n")
            sleep.chmod(0o755)
            env = {**os.environ, "CODEGEN_TEST_ROOT": str(root), "PATH": f"{root}{os.pathsep}{os.environ['PATH']}"}
            result = subprocess.run(["bash", str(script), str(fake_go)], env=env, capture_output=True, text=True, check=True)
            self.assertIn("temporary module download failure", result.stderr)
            self.assertEqual((root / "download-calls").read_text(), "2")
            self.assertTrue((root / "downloaded").exists())

    def test_custom_linter_rebuilds_on_configuration_and_toolchain_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            config = root / "hack/testing/kal-linter/.custom-gcl.yaml"
            config.parent.mkdir(parents=True)
            config.write_text("version: v1\n")
            (root / "bin").mkdir()
            fake_go = root / "go"
            fake_go.write_text('#!/bin/sh\nprintf "%s\\n" "${KAL_TEST_VERSION:-go1.27.0}"\nif [ "$#" -gt 2 ]; then printf "%s\\n" linux amd64; fi\n')
            fake_go.chmod(0o755)
            linter = root / "bin/golangci-lint"
            linter.write_text('''#!/bin/sh
set -e
if [ "$1" = version ]; then echo test-linter; exit 0; fi
mkdir -p bin
printf '#!/bin/sh\\nexit 0\\n' > bin/golangci-lint-kube-api-linter
chmod +x bin/golangci-lint-kube-api-linter
echo built >> "$KAL_TEST_LOG"
''')
            linter.chmod(0o755)
            makefile = root / "Makefile"
            makefile.write_text(f"GO_CMD := {fake_go}\nBIN_DIR := {root / 'bin'}\nNETWORK_INSTALL_RETRY := env\ninclude {Path(__file__).resolve().parents[1] / 'make/deps.mk'}\n")
            env = {**os.environ, "KAL_TEST_LOG": str(root / "builds")}
            command = ["make", "-s", "-o", "golangci-lint", "NETWORK_INSTALL_RETRY=env", "golangci-lint-kal"]
            for _ in range(2):
                subprocess.run(command, cwd=root, env=env, check=True)
            self.assertEqual((root / "builds").read_text().splitlines(), ["built"])
            config.write_text("version: v2\n")
            subprocess.run(command, cwd=root, env=env, check=True)
            subprocess.run(command, cwd=root, env={**env, "KAL_TEST_VERSION": "go1.27.1"}, check=True)
            self.assertEqual((root / "builds").read_text().splitlines(), ["built"] * 3)


if __name__ == "__main__":
    unittest.main()
