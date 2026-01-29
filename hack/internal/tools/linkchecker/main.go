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
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// serverProcess wraps exec.Cmd with captured output and exit monitoring.
type serverProcess struct {
	cmd      *exec.Cmd
	stderr   *bytes.Buffer
	stderrMu sync.Mutex
	exitErr  chan error
}

var (
	portFlag   = flag.Int("port", 0, "port to run the Hugo server on (0 = auto-pick a free port)")
	threads    = flag.Int("threads", 20, "linkchecker threads")
	timeoutSec = flag.Int("timeout", 5, "linkchecker per-request timeout in seconds")
	// Default FALSE (more reliable for CI; avoids rate-limits / flaky external sites)
	checkExt = flag.Bool("check-extern", false, "check external links (default: false)")
	waitFor  = flag.Duration("wait", 5*time.Minute, "max time to wait for the server to become reachable")
	lcTotal  = flag.Duration("linkchecker-total-timeout", 25*time.Minute, "total time allowed for linkchecker")
)

func main() {
	flag.Parse()
	os.Exit(run())
}

func run() int {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}

	// Keep Hugo caches/resources out of the repo so `make verify` doesn't see untracked files.
	tmp, err := os.MkdirTemp("", "kueue-website-links-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := ensureTools(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}

	// Install npm dependencies if needed.
	if err := runNpmCI(root); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}

	port := *portFlag
	if port == 0 {
		// Auto-pick a free port (will avoid conflicts automatically).
		port, err = pickFreePort()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			return 1
		}
	} else if !isPortFree(port) {
		// User specified a port - check if it's free, otherwise pick a new one.
		fmt.Printf("==> Port %d is busy, picking a free port instead\n", port)
		port, err = pickFreePort()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			return 1
		}
	}
	baseURL := fmt.Sprintf("http://localhost:%d/", port)

	// CI-friendly hugo server flags.
	hugoServerArgs := []string{
		"--bind", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--disableFastRender",
		"--disableLiveReload",
		"--watch=false",
		"--minify",
		"--environment", "production",
	}

	fmt.Printf("==> Starting site server via `hugo server` on %s\n", baseURL)
	fmt.Printf("    Hugo args: %q\n", strings.Join(hugoServerArgs, " "))

	server, err := startSiteServer(root, tmp, hugoServerArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	defer server.stop()

	fmt.Printf("==> Waiting for %s to be reachable (timeout: %s)\n", baseURL, *waitFor)
	serverCtx, serverCancel := context.WithTimeout(context.Background(), *waitFor)
	defer serverCancel()

	if err := waitForHTTPOrExit(serverCtx, baseURL, server); err != nil {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "========================================")
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		fmt.Fprintln(os.Stderr, "========================================")

		if stderr := server.getStderr(); stderr != "" {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Server stderr output:")
			fmt.Fprintln(os.Stderr, "----------------------------------------")
			fmt.Fprintln(os.Stderr, stderr)
			fmt.Fprintln(os.Stderr, "----------------------------------------")
		}

		if stderr := server.getStderr(); strings.Contains(stderr, "postcss") || strings.Contains(stderr, "PostCSS") {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "HINT: PostCSS is not installed. Run 'npm ci' in the site/ directory.")
		}

		return 1
	}

	// Start URL normalizing proxy to handle case-sensitive language paths.
	fmt.Println("==> Starting URL normalizing proxy")
	proxy, proxyPort, err := startURLNormalizingProxy(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	defer proxy.stop()

	proxyURL := fmt.Sprintf("http://localhost:%d/", proxyPort)
	fmt.Printf("    Proxy listening on %s (forwarding to Hugo on port %d)\n", proxyURL, port)

	proxyCtx, proxyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer proxyCancel()
	if err := waitForProxy(proxyCtx, proxyURL); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}

	fmt.Println("==> Running linkchecker")
	lcCtx, lcCancel := context.WithTimeout(context.Background(), *lcTotal)
	defer lcCancel()

	exitCode := runLinkChecker(lcCtx, proxyPort, *threads, *timeoutSec, *checkExt)
	if exitCode != 0 {
		return exitCode
	}
	fmt.Println("==> OK: linkchecker passed")
	return 0
}

func ensureTools() error {
	if _, err := exec.LookPath("linkchecker"); err != nil {
		return errors.New("linkchecker not found in PATH; ensure the linkchecker Docker image provides it")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return errors.New("npm not found in PATH; ensure the linkchecker Docker image provides it")
	}
	if _, err := exec.LookPath("hugo"); err != nil {
		return errors.New("hugo not found in PATH; ensure the linkchecker Docker image provides it")
	}
	return nil
}

// runNpmCI runs 'npm ci' in the site directory if a package.json is present.
func runNpmCI(repoRoot string) error {
	if skip := strings.TrimSpace(os.Getenv("SKIP_NPM_CI")); skip != "" && skip != "0" {
		fmt.Println("==> SKIP_NPM_CI is set, skipping npm ci")
		return nil
	}

	siteDir := filepath.Join(repoRoot, "site")
	if !exists(filepath.Join(siteDir, "package.json")) {
		fmt.Println("==> No package.json in site/, skipping npm ci")
		return nil
	}

	fmt.Println("==> Running npm ci in site/")

	cmd := exec.Command("npm", "ci")
	cmd.Dir = siteDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm ci failed: %w", err)
	}

	fmt.Println("    npm ci completed")
	return nil
}

func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
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

func startSiteServer(repoRoot, tmpDir string, hugoServerArgs []string) (*serverProcess, error) {
	hugoPath := strings.TrimSpace(os.Getenv("HUGO"))
	if hugoPath == "" {
		var err error
		hugoPath, err = exec.LookPath("hugo")
		if err != nil {
			return nil, errors.New("hugo not found in PATH")
		}
	}

	args := append([]string{"server"}, hugoServerArgs...)
	cmd := exec.Command(hugoPath, args...)
	cmd.Dir = filepath.Join(repoRoot, "site")

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
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		fmt.Sprintf("TMPDIR=%s", tmpDir),
	)
	cmd.Env = env

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
		select {
		case err := <-server.exitErr:
			return fmt.Errorf("server failed to start: %w", err)
		default:
		}

		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil
		}

		select {
		case err := <-server.exitErr:
			return fmt.Errorf("server failed to start: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for server at %s: %w", url, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForProxy(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for proxy at %s: %w", url, ctx.Err())
		case <-ticker.C:
		}
	}
}

func runLinkChecker(ctx context.Context, port, threads, timeoutSec int, checkExtern bool) int {
	args := []string{
		"--no-warnings",
		fmt.Sprintf("--threads=%d", threads),
		fmt.Sprintf("--timeout=%d", timeoutSec),
		"--ignore-url=^mailto:",
		"--ignore-url=^tel:",
	}

	if checkExtern {
		args = append(args,
			"--check-extern",
			"--no-follow-url=^https?://(?!localhost)",
		)
	}

	args = append(args, fmt.Sprintf("http://localhost:%d/", port))

	cmd := exec.CommandContext(ctx, "linkchecker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "ERROR: linkchecker timed out: %v\n", ctx.Err())
			return 1
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
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

// urlNormalizingProxy is a reverse proxy that normalizes URL paths.
type urlNormalizingProxy struct {
	proxy  *httputil.ReverseProxy
	server *http.Server
}

var urlPathNormalizers = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`^/zh-CN/`), "/zh-cn/"},
}

func startURLNormalizingProxy(hugoPort int) (*urlNormalizingProxy, int, error) {
	proxyPort, err := pickFreePort()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to pick port for proxy: %w", err)
	}

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", hugoPort))
	proxy := httputil.NewSingleHostReverseProxy(target)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		for _, normalizer := range urlPathNormalizers {
			if normalizer.pattern.MatchString(req.URL.Path) {
				req.URL.Path = normalizer.pattern.ReplaceAllString(req.URL.Path, normalizer.replacement)
				break
			}
		}
	}

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", proxyPort),
		Handler: proxy,
	}

	unp := &urlNormalizingProxy{
		proxy:  proxy,
		server: server,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "proxy server error: %v\n", err)
		}
	}()

	return unp, proxyPort, nil
}

func (p *urlNormalizingProxy) stop() {
	if p == nil || p.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
}
