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
import json
import os
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import call, patch
from urllib.error import HTTPError, URLError

SPEC = importlib.util.spec_from_file_location("installer", Path(__file__).with_name("install-tool.py"))
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)


class InstallTest(unittest.TestCase):
    def test_reuse_and_upgrade(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(installer.subprocess, "check_output", return_value="linux\namd64\n"):
            binary = Path(temp) / "controller-gen"
            with patch.object(installer, "release_binary", return_value=b"binary") as download:
                installer.install("controller-gen", "v1", binary)
                installer.install("controller-gen", "v1", binary)
                self.assertEqual(download.call_count, 1)
                installer.install("controller-gen", "v2", binary)
                self.assertEqual(download.call_count, 2)
                binary.unlink()
                installer.install("controller-gen", "v2", binary)
                self.assertEqual(download.call_count, 3)

    def test_failed_upgrade_preserves_installed_version(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(installer.subprocess, "check_output", return_value="linux\namd64\n"):
            binary = Path(temp) / "controller-gen"
            binary.write_bytes(b"old")
            with patch.object(installer, "release_binary", side_effect=ValueError("SHA256 mismatch")):
                with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                    installer.install("controller-gen", "v2", binary)
            self.assertEqual(binary.read_bytes(), b"old")
            self.assertFalse(binary.with_name("controller-gen.release-version").exists())

    def metadata(self, content, digest=True):
        filename = "controller-gen-linux-amd64"
        asset = {"name": filename, "browser_download_url": "https://example.test/binary"}
        if digest:
            asset["digest"] = "sha256:" + hashlib.sha256(content).hexdigest()
        return {"assets": [asset]}

    def test_binary_digest(self):
        content = b"release binary"
        with patch.object(installer, "download", side_effect=[json.dumps(self.metadata(content)).encode(), content]):
            self.assertEqual(installer.release_binary("controller-gen", "v1", "linux", "amd64"), content)

    def test_corrupt_binary_is_rejected(self):
        with patch.object(installer, "download", side_effect=[json.dumps(self.metadata(b"correct")).encode(), b"corrupt"]):
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                installer.release_binary("controller-gen", "v1", "linux", "amd64")

    def test_older_release_checksums(self):
        content = b"old binary"
        for checksum in (
            hashlib.sha256(content).hexdigest() + "  controller-gen-linux-amd64",
            "SHA256 (controller-gen-linux-amd64) = " + hashlib.sha256(content).hexdigest(),
        ):
            with self.subTest(checksum=checksum):
                metadata = self.metadata(content, digest=False)
                metadata["assets"].append({"name": "checksums.txt", "browser_download_url": "https://example.test/checksums"})
                with patch.object(installer, "download", side_effect=[json.dumps(metadata).encode(), content, checksum.encode()]):
                    self.assertEqual(installer.release_binary("controller-gen", "v1", "linux", "amd64"), content)

    def test_missing_checksum_is_rejected(self):
        with patch.object(installer, "download", side_effect=[json.dumps(self.metadata(b"", digest=False)).encode(), b""]):
            with self.assertRaisesRegex(ValueError, "No SHA256 checksum"):
                installer.release_binary("controller-gen", "v1", "linux", "amd64")

    def test_missing_asset_is_distinct_from_download_errors(self):
        for name in ("controller-gen", "kind"):
            with self.subTest(name=name), patch.object(installer, "download", return_value=b'{"assets": []}') as download:
                with self.assertRaisesRegex(installer.NoReleaseBinary, f"has no {name}"):
                    installer.release_binary(name, "v1", "linux", "amd64")
                download.assert_called_once()

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
        packages = {
            "golangci-lint": ("github.com/golangci/golangci-lint/v2", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"),
            "controller-gen": ("sigs.k8s.io/controller-tools", "sigs.k8s.io/controller-tools/cmd/controller-gen"),
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
                self.assertEqual(run.call_args.args[0][-3:], [module, "v1", package])

    def test_source_build_failure_logs_compiler_output(self):
        import subprocess

        result = subprocess.CompletedProcess(["go", "install"], 1, stdout="compiler failure details\n")
        with patch.object(installer.subprocess, "run", return_value=result), self.assertLogs(installer.LOG, level="DEBUG") as logs:
            with self.assertRaises(subprocess.CalledProcessError):
                installer.install_source("helm-docs", "v1", "/tmp/helm-docs")
            self.assertIn("compiler failure details", "\n".join(logs.output))

    def test_artifact_cleanup_preserves_installer_diagnostics(self):
        import subprocess

        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            log = root / "install-kind.log"
            log.write_text("download diagnostics\n")
            (root / "manifests.yaml").touch()
            (root / ".hidden").touch()
            (root / "nested").mkdir()
            (root / "nested" / "binary").touch()
            # Run the actual target in isolation from unrelated Makefile prerequisites.
            makefile = Path(__file__).resolve().parents[1] / "Makefile"
            recipe = makefile.read_text().split("clean-artifacts:\n", 1)[1].split("\n\n", 1)[0]
            command = ["make", "-f", "-", "clean-artifacts", f"ARTIFACTS={root}"]
            subprocess.run(command, input="clean-artifacts:\n" + recipe + "\n", text=True, check=True, capture_output=True)
            self.assertEqual(list(root.iterdir()), [log])
            self.assertEqual(log.read_text(), "download diagnostics\n")

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
                manifest = f"SHA256 ({filename}) = {'a' * 64}\n".encode()
                with patch.object(installer, "download", return_value=manifest) as download:
                    assets = installer.release_assets(name, "v1", repo, tag)
                    self.assertEqual(assets[0]["name"], filename)
                    self.assertEqual(assets[0]["digest"], "sha256:" + "a" * 64)
                    self.assertTrue(download.call_args.args[0].startswith("https://github.com/"))

    def test_invalid_checksum_manifest_is_not_a_missing_binary(self):
        with patch.object(installer, "download", return_value=b"unrecognized format"):
            with self.assertRaisesRegex(ValueError, "No SHA256 checksums") as error:
                installer.release_binary("mdtoc", "v1", "linux", "amd64")
            self.assertNotIsInstance(error.exception, installer.NoReleaseBinary)

    def test_partial_manifest_does_not_hide_missing_checksum(self):
        manifest = f"{'a' * 64}  mdtoc-arm64-linux\n".encode()
        published = json.dumps({"assets": [{"name": "mdtoc-amd64-linux"}]}).encode()
        with patch.object(installer, "download", side_effect=[manifest, published]):
            with self.assertRaisesRegex(ValueError, "No SHA256 checksum") as error:
                installer.release_binary("mdtoc", "v1", "linux", "amd64")
            self.assertNotIsInstance(error.exception, installer.NoReleaseBinary)

    def test_manifest_without_host_asset_allows_source_build(self):
        manifest = f"{'a' * 64}  mdtoc-arm64-linux\n".encode()
        with patch.object(installer, "download", side_effect=[manifest, b'{"assets": []}']):
            with self.assertRaises(installer.NoReleaseBinary):
                installer.release_binary("mdtoc", "v1", "linux", "s390x")

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

    def test_download_scopes_credentials_to_api(self):
        with patch.dict(os.environ, {"GH_TOKEN": "test-token"}):
            for url, token in [("https://api.github.com/repos/example/tool", "Bearer test-token"), ("https://example.test/binary", None)]:
                with self.subTest(url=url), patch.object(installer, "urlopen", return_value=io.BytesIO(b"binary")) as request:
                    self.assertEqual(installer.download(url), b"binary")
                    self.assertEqual(request.call_args.args[0].get_header("Authorization"), token)

    def test_cli_logs_reuse_and_failure_to_artifacts(self):
        import subprocess

        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fake_go = root / "go"
            fake_go.write_text("#!/bin/sh\nprintf 'linux\\namd64\\n'\n")
            fake_go.chmod(0o755)
            binary = root / "controller-gen"
            binary.write_bytes(b"binary")
            binary.chmod(0o755)
            (root / "controller-gen.release-version").write_text("v1 linux/amd64\n")
            artifacts = root / "custom-artifacts"
            env = {**os.environ, "GO_CMD": str(fake_go), "ARTIFACTS": str(artifacts), "GH_TOKEN": "test-secret"}
            script = str(Path(__file__).with_name("install-tool.py"))
            for name, status, message in [("controller-gen", 0, "Reusing controller-gen"), ("unknown", 1, "No upstream binary configured")]:
                result = subprocess.run([sys.executable, script, name, "v1", str(root / name)], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, status, result.stderr)
                log = (artifacts / f"install-{name}.log").read_text()
                self.assertIn(message, log)
                self.assertIn(message, result.stderr)
                self.assertNotIn("test-secret", log)
                if status:
                    self.assertIn("Traceback", log)

    def test_supported_release_names(self):
        expected = {
            "golangci-lint": ("golangci/golangci-lint", "v1.2.3", "golangci-lint-1.2.3-darwin-arm64.tar.gz"),
            "controller-gen": ("kubernetes-sigs/controller-tools", "v1.2.3", "controller-gen-darwin-arm64"),
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
            "asset": "example-tool_{version}_{goos}_{goarch}.tar.gz",
            "checksum": "checksums-{number}.txt",
        }
        with patch.dict(installer.TOOLS, {"example-tool": tool}):
            repo, tag, asset = installer.release("example-tool", "v2.3.4", "linux", "amd64")
            self.assertEqual((repo, tag, asset), ("example/tools", "tool/v2.3.4", "example-tool_v2.3.4_linux_amd64.tar.gz"))
            manifest = f"{'a' * 64}  {asset}".encode()
            with patch.object(installer, "download", return_value=manifest) as download:
                assets = installer.release_assets("example-tool", "v2.3.4", repo, tag)
                download.assert_called_once_with("https://github.com/example/tools/releases/download/tool%2Fv2.3.4/checksums-2.3.4.txt")
                self.assertEqual(assets[0]["name"], asset)
            with patch.object(installer.subprocess, "run") as run:
                installer.install_source("example-tool", "v2.3.4", "/tmp/example-tool")
                self.assertEqual(run.call_args.args[0][-3:], ["example.test/tools", "v2.3.4", "example.test/tools/cmd/example-tool"])

    def test_source_tool_rebuilds_only_when_required(self):
        import subprocess

        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            fake_go = directory / "go"
            fake_go.write_text('''#!/usr/bin/env python3
import os, pathlib, sys
if sys.argv[1] == "env":
    values = {"GOVERSION": "go1.27.0", "GOHOSTOS": "linux", "GOHOSTARCH": "amd64", "GOFLAGS": os.environ.get("GOFLAGS", "")}
    print("\\n".join(values[key] for key in sys.argv[2:]))
else:
    assert os.environ["GOOS"] == "linux" and os.environ["GOARCH"] == "amd64"
    binary = pathlib.Path(os.environ["GOBIN"]) / "generator"
    with binary.with_suffix(".calls").open("a") as calls:
        calls.write(sys.argv[-1] + "\\n")
    binary.write_text("binary")
    binary.chmod(0o755)
''')
            fake_go.chmod(0o755)
            env = {**os.environ, "GO_CMD": str(fake_go), "GOOS": "windows", "GOARCH": "386"}
            command = ["bash", str(Path(__file__).with_name("install-source-tool.sh")), str(directory / "generator"), "example/generator", "v1", "example/generator"]
            subprocess.run(command, env=env, check=True)
            subprocess.run(command, env=env, check=True)
            command[-2] = "v2"
            subprocess.run(command, env=env, check=True)
            env["GOFLAGS"] = "-tags=custom"
            subprocess.run(command, env=env, check=True)
            subprocess.run(command, env=env, check=True)
            command[-1] = "example/generator/cmd/generator"
            subprocess.run(command, env=env, check=True)
            self.assertEqual((directory / "generator.calls").read_text(), "example/generator@v1\nexample/generator@v2\nexample/generator@v2\nexample/generator/cmd/generator@v2\n")

    def test_codegen_downloads_module_before_querying_source_directory(self):
        import shutil
        import subprocess

        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            script = root / "hack/tools/code-generator/generate.sh"
            script.parent.mkdir(parents=True)
            shutil.copyfile(Path(__file__).parent / "tools/code-generator/generate.sh", script)
            module = root / "module"
            module.mkdir()
            (module / "kube_codegen.sh").write_text("\n".join(f"kube::codegen::{name}() {{ :; }}" for name in ["gen_helpers", "gen_openapi", "gen_client"]))
            fake_go = root / "go"
            fake_go.write_text('''#!/usr/bin/env python3
import os, pathlib, sys
root = pathlib.Path(os.environ["CODEGEN_TEST_ROOT"])
if sys.argv[1:] == ["mod", "download", "k8s.io/code-generator"]:
    (root / "downloaded").touch()
elif sys.argv[1] == "list" and (root / "downloaded").exists():
    print(root / "module")
else:
    sys.exit(1)
''')
            fake_go.chmod(0o755)
            subprocess.run(["bash", str(script), str(fake_go)], env={**os.environ, "CODEGEN_TEST_ROOT": str(root)}, check=True)
            self.assertTrue((root / "downloaded").exists())

    def test_custom_linter_rebuilds_on_configuration_and_toolchain_changes(self):
        import subprocess

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
            makefile.write_text(f"GO_CMD := {fake_go}\nBIN_DIR := {root / 'bin'}\nNETWORK_INSTALL_RETRY := env\ninclude {Path(__file__).resolve().parent / 'make/deps.mk'}\n")
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
