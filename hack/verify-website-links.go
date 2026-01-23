/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const nodeVersion = "v20.18.0"

// serverProcess wraps exec.Cmd with captured output and exit monitoring
type serverProcess struct {
	cmd      *exec.Cmd
	stderr   *bytes.Buffer
	stderrMu sync.Mutex
	exitErr  chan error
}

func main() {
	var (
		portFlag   = flag.Int("port", 0, "port to run the Hugo server on (0 = auto-pick a free port)")
		threads    = flag.Int("threads", 10, "linkchecker threads")
		timeoutSec = flag.Int("timeout", 20, "linkchecker per-request timeout in seconds")
		// Default FALSE (more reliable for CI; avoids rate-limits / flaky external sites)
		checkExt = flag.Bool("check-extern", false, "check external links (default: false)")
		waitFor  = flag.Duration("wait", 5*time.Minute, "max time to wait for the server to become reachable")
		lcTotal  = flag.Duration("linkchecker-total-timeout", 25*time.Minute, "total time allowed for linkchecker")
	)
	flag.Parse()

	root, err := repoRoot()
	must(err)

	// Keep Hugo caches/resources out of the repo so `make verify` doesn't see untracked files.
	tmp, err := os.MkdirTemp("", "kueue-website-links-*")
	must(err)
	defer func() { _ = os.RemoveAll(tmp) }()

	// Ensure Node.js is available (required for PostCSS)
	pathEnv, err := ensureNodeJS(tmp)
	must(err)

	// Install npm dependencies
	must(runNpmCI(root, pathEnv))

	port := *portFlag
	if port == 0 {
		// Auto-pick a free port (will avoid conflicts automatically)
		port, err = pickFreePort()
		must(err)
	} else if !isPortFree(port) {
		// User specified a port - check if it's free, otherwise pick a new one
		fmt.Printf("==> Port %d is busy, picking a free port instead\n", port)
		port, err = pickFreePort()
		must(err)
	}
	baseURL := fmt.Sprintf("http://localhost:%d/", port)

	// CI-friendly hugo server flags, injected through Makefile's HUGO_SERVER_ARGS.
	hugoServerArgs := strings.Join([]string{
		"--bind", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--disableFastRender",
		"--disableLiveReload",
		"--watch=false",
		"--minify",
		"--environment", "production",
	}, " ")

	fmt.Printf("==> Starting site server via `make site-server` on %s\n", baseURL)
	fmt.Printf("    HUGO_SERVER_ARGS=%q\n", hugoServerArgs)

	server, err := startSiteServer(root, tmp, hugoServerArgs, pathEnv)
	must(err)
	defer server.stop()

	fmt.Printf("==> Waiting for %s to be reachable (timeout: %s)\n", baseURL, *waitFor)
	waitCtx, cancel := context.WithTimeout(context.Background(), *waitFor)
	defer cancel()

	if err := waitForHTTPOrExit(waitCtx, baseURL, server); err != nil {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "========================================")
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		fmt.Fprintln(os.Stderr, "========================================")

		// Print captured stderr if available
		if stderr := server.getStderr(); stderr != "" {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Server stderr output:")
			fmt.Fprintln(os.Stderr, "----------------------------------------")
			fmt.Fprintln(os.Stderr, stderr)
			fmt.Fprintln(os.Stderr, "----------------------------------------")
		}

		// Check for common issues
		if stderr := server.getStderr(); strings.Contains(stderr, "postcss") || strings.Contains(stderr, "PostCSS") {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "HINT: PostCSS is not installed. Run 'npm ci' in the site/ directory.")
		}

		os.Exit(1)
	}

	fmt.Println("==> Running linkchecker")
	lcCtx, lcCancel := context.WithTimeout(context.Background(), *lcTotal)
	defer lcCancel()

	exitCode := runLinkChecker(lcCtx, tmp, port, *threads, *timeoutSec, *checkExt)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	fmt.Println("==> OK: linkchecker passed")
}

// nodeBinDir holds the path to installed Node.js binaries (set by ensureNodeJS)
var nodeBinDir string

// ensureNodeJS checks if Node.js is available, installs it if not, and returns the PATH
// that should be used for subsequent commands.
func ensureNodeJS(tmpDir string) (string, error) {
	// Check if npm is already available
	if _, err := exec.LookPath("npm"); err == nil {
		fmt.Println("==> Node.js is already available")
		return os.Getenv("PATH"), nil
	}

	fmt.Println("==> Node.js not found, installing...")

	// Determine architecture
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}

	// Determine OS
	var osName string
	switch runtime.GOOS {
	case "linux":
		osName = "linux"
	case "darwin":
		osName = "darwin"
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	// Download Node.js (use tar.gz for easier handling in Go)
	nodeDir := filepath.Join(tmpDir, fmt.Sprintf("node-%s-%s-%s", nodeVersion, osName, arch))
	tarball := fmt.Sprintf("node-%s-%s-%s.tar.gz", nodeVersion, osName, arch)
	url := fmt.Sprintf("https://nodejs.org/dist/%s/%s", nodeVersion, tarball)

	fmt.Printf("    Downloading %s\n", url)

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to download Node.js: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download Node.js: HTTP %d", resp.StatusCode)
	}

	// Extract tarball
	if err := extractTarGz(resp.Body, tmpDir); err != nil {
		return "", fmt.Errorf("failed to extract Node.js: %w", err)
	}

	nodeBinDir = filepath.Join(nodeDir, "bin")
	newPath := nodeBinDir + string(os.PathListSeparator) + os.Getenv("PATH")

	fmt.Printf("    Node.js installed to %s\n", nodeBinDir)
	return newPath, nil
}

// extractTarGz extracts a .tar.gz archive to the destination directory
func extractTarGz(r io.Reader, destDir string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			// Remove existing symlink if it exists
			os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// runNpmCI runs 'npm ci' in the site directory
func runNpmCI(repoRoot, pathEnv string) error {
	siteDir := filepath.Join(repoRoot, "site")

	// Check if package.json exists
	if !exists(filepath.Join(siteDir, "package.json")) {
		fmt.Println("==> No package.json in site/, skipping npm ci")
		return nil
	}

	fmt.Println("==> Running npm ci in site/")

	npmPath := "npm"
	if nodeBinDir != "" {
		npmPath = filepath.Join(nodeBinDir, "npm")
	}

	cmd := exec.Command(npmPath, "ci")
	cmd.Dir = siteDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "PATH="+pathEnv)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm ci failed: %w", err)
	}

	fmt.Println("    npm ci completed")
	return nil
}

func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false // Port is in use
	}
	ln.Close()
	return true
}

func repoRoot() (string, error) {
	if _, err := exec.LookPath("git"); err == nil {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if exists(filepath.Join(dir, ".git")) || exists(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not determine repo root (no .git/go.mod found)")
		}
		dir = parent
	}
}

func pickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func startSiteServer(repoRoot, tmpDir, hugoServerArgs, pathEnv string) (*serverProcess, error) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		return nil, errors.New("make not found in PATH")
	}

	cmd := exec.Command(makePath, "site-server")
	cmd.Dir = repoRoot

	// Capture stderr to a buffer while also printing to os.Stderr
	stderrBuf := &bytes.Buffer{}
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, stderrBuf)

	// Build environment, filtering out existing PATH to replace it
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PATH=") {
			env = append(env, e)
		}
	}
	env = append(env,
		// Use the PATH that includes Node.js
		fmt.Sprintf("PATH=%s", pathEnv),

		// Makefile hook: site-server runs `hugo server $(HUGO_SERVER_ARGS)`
		fmt.Sprintf("HUGO_SERVER_ARGS=%s", hugoServerArgs),

		// Keep artifacts out of repo.
		fmt.Sprintf("TMPDIR=%s", tmpDir),
	)
	cmd.Env = env

	// Allow killing the whole process group on Unix.
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sp := &serverProcess{
		cmd:     cmd,
		stderr:  stderrBuf,
		exitErr: make(chan error, 1),
	}

	// Monitor for process exit
	go func() {
		err := cmd.Wait()
		if err != nil {
			sp.exitErr <- fmt.Errorf("server process exited: %w", err)
		} else {
			sp.exitErr <- errors.New("server process exited unexpectedly (exit code 0)")
		}
	}()

	return sp, nil
}

func (sp *serverProcess) getStderr() string {
	sp.stderrMu.Lock()
	defer sp.stderrMu.Unlock()
	return sp.stderr.String()
}

func (sp *serverProcess) stop() {
	if sp == nil || sp.cmd == nil || sp.cmd.Process == nil {
		return
	}

	// Graceful first.
	if runtime.GOOS != "windows" {
		_ = syscall.Kill(-sp.cmd.Process.Pid, syscall.SIGTERM)
		_ = sp.cmd.Process.Signal(syscall.SIGTERM)
	} else {
		_ = sp.cmd.Process.Kill()
	}

	select {
	case <-sp.exitErr:
		return
	case <-time.After(5 * time.Second):
		// Hard kill.
		if runtime.GOOS != "windows" {
			_ = syscall.Kill(-sp.cmd.Process.Pid, syscall.SIGKILL)
			_ = sp.cmd.Process.Signal(syscall.SIGKILL)
		} else {
			_ = sp.cmd.Process.Kill()
		}
		<-sp.exitErr
	}
}

// waitForHTTPOrExit waits for the HTTP server to become reachable, but also
// monitors for early server exit (e.g., due to build failure).
func waitForHTTPOrExit(ctx context.Context, url string, server *serverProcess) error {
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Check if server process has exited
		select {
		case err := <-server.exitErr:
			return fmt.Errorf("server failed to start: %w", err)
		default:
		}

		// Try to connect
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil
		}

		// Wait for next tick or exit
		select {
		case err := <-server.exitErr:
			return fmt.Errorf("server failed to start: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for server at %s: %w", url, ctx.Err())
		case <-ticker.C:
			// Continue polling
		}
	}
}

func ensureLinkChecker(tmpDir string) (string, error) {
	// Check if linkchecker is already available
	if lcPath, err := exec.LookPath("linkchecker"); err == nil {
		fmt.Println("==> linkchecker is available")
		return lcPath, nil
	}

	fmt.Println("==> linkchecker not found, installing...")

	// Try pipx first (recommended for CLI tools on modern systems)
	if pipxPath, err := exec.LookPath("pipx"); err == nil {
		fmt.Println("    Using pipx to install linkchecker...")
		cmd := exec.Command(pipxPath, "install", "linkchecker")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			// pipx installs to ~/.local/bin by default
			home, _ := os.UserHomeDir()
			lcPath := filepath.Join(home, ".local/bin/linkchecker")
			if exists(lcPath) {
				fmt.Println("    linkchecker installed successfully via pipx")
				return lcPath, nil
			}
			// Also check if it's now in PATH
			if lcPath, err := exec.LookPath("linkchecker"); err == nil {
				fmt.Println("    linkchecker installed successfully via pipx")
				return lcPath, nil
			}
		}
		// pipx failed, fall through to venv method
		fmt.Println("    pipx install failed, trying venv method...")
	}

	// Create a virtual environment and install linkchecker there
	fmt.Println("    Creating virtual environment for linkchecker...")

	// Find python3
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		return "", errors.New("python3 not found in PATH")
	}

	// Create venv in temp directory
	venvDir := filepath.Join(tmpDir, "linkchecker-venv")

	cmd := exec.Command(pythonPath, "-m", "venv", venvDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create venv: %w", err)
	}

	// Install linkchecker in the venv
	venvPip := filepath.Join(venvDir, "bin/pip")
	cmd = exec.Command(venvPip, "install", "--quiet", "linkchecker")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to install linkchecker in venv: %w", err)
	}

	lcPath := filepath.Join(venvDir, "bin/linkchecker")
	if !exists(lcPath) {
		return "", errors.New("linkchecker not found in venv after installation")
	}

	fmt.Println("    linkchecker installed successfully in venv")
	return lcPath, nil
}

func runLinkChecker(ctx context.Context, tmpDir string, port, threads, timeoutSec int, checkExtern bool) int {
	lcPath, err := ensureLinkChecker(tmpDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}

	args := []string{
		"--no-warnings",
		fmt.Sprintf("--threads=%d", threads),
		fmt.Sprintf("--timeout=%d", timeoutSec),
		"--ignore-url=^mailto:",
		"--ignore-url=^tel:",
	}

	if checkExtern {
		// External checks are flaky; keep opt-in.
		args = append(args,
			"--check-extern",
			"--no-follow-url=^https?://(?!localhost)",
		)
	}

	args = append(args, fmt.Sprintf("http://localhost:%d/", port))

	cmd := exec.CommandContext(ctx, lcPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "ERROR: linkchecker timed out: %v\n", ctx.Err())
			return 1
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "ERROR: failed to run linkchecker: %v\n", err)
		return 1
	}
	return 0
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		panic(err)
	}
}
