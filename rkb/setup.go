package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// setup: make the device shell able to run every check.
//
// Inside the Claude desktop VM the network allowlist blocks go.dev,
// golang.org and proxy.golang.org but allows github.com. So:
//   1. Go toolchain: download the tarball GitHub Actions publishes
//      (github.com/actions/go-versions) into ~/.local/go.
//   2. golang.org/x/* modules: clone their GitHub mirrors and write a local
//      module proxy (~/goproxy). go.sum still verifies every zip.
// goEnv() points every Go invocation (including validate_metrics' `go test`)
// at both. On a normal Mac with Go installed, setup is a no-op.

func home() string { h, _ := os.UserHomeDir(); return h }

// goBinary: the Go that setup installed (~/.local/go) when the shell's PATH
// doesn't have one; exec resolves the name against rkb's own PATH, not the
// child's environment.
func goBinary() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	if g := filepath.Join(home(), ".local", "go", "bin", "go"); fileExists(g) {
		return g
	}
	return "go"
}

func goEnv() []string {
	var env []string
	gobin := filepath.Join(home(), ".local", "go", "bin")
	if dirExists(gobin) {
		env = append(env, "PATH="+gobin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if dirExists(filepath.Join(home(), "goproxy")) {
		env = append(env, "GOPROXY=file://"+filepath.Join(home(), "goproxy")+",direct", "GOSUMDB=off",
			"GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly")
	}
	if os.Getenv("VALIDATE_PYTHON") == "" && fileExists(pyVenv()) {
		env = append(env, "VALIDATE_PYTHON="+pyVenv())
	}
	return env
}

// pyVenv: the interpreter setup provisions for the projects' own Python
// checks (figure-derivation scripts, benchmarks, analysis notebooks) when the
// device shell has no Python >= 3.11 with numpy and torch.
func pyVenv() string { return filepath.Join(home(), "rkb-py", "bin", "python") }

func pythonReady(py string) bool {
	return exec.Command(py, "-c", "import sys, numpy, torch; sys.exit(0 if sys.version_info >= (3, 11) else 1)").Run() == nil
}

// installPython: Python 3.12 + numpy + CPU torch via uv (uv comes from PyPI,
// its Python builds from GitHub — both reachable from the desktop VM, where
// download.pytorch.org is not). On Linux arm64, PyPI's torch 2.5.1 is the
// last CPU-only build; newer ones pull gigabytes of CUDA libraries.
func installPython() {
	if fileExists(pyVenv()) && pythonReady(pyVenv()) {
		fmt.Println("python: ~/rkb-py ready (numpy, torch)")
		return
	}
	if runtime.GOOS != "linux" {
		fmt.Println("python: checks use each repo's .venv, or set VALIDATE_PYTHON to a Python >= 3.11 with numpy and torch")
		return
	}
	uv := filepath.Join(home(), ".local", "bin", "uv")
	run := func(name string, args ...string) error {
		c := exec.Command(name, args...)
		c.Env = append(os.Environ(), "UV_CACHE_DIR="+filepath.Join(home(), ".cache", "uv"))
		o, err := c.CombinedOutput()
		if err != nil {
			fmt.Printf("python: %s %s failed: %v\n%s\n", name, strings.Join(args, " "), err, truncate(string(o), 400))
		}
		return err
	}
	if !fileExists(uv) && run("python3", "-m", "pip", "install", "--user", "--quiet", "uv") != nil {
		return
	}
	torch := "torch"
	if runtime.GOARCH == "arm64" {
		torch = "torch==2.5.1"
	}
	if run(uv, "python", "install", "3.12") != nil ||
		run(uv, "venv", "--allow-existing", "--python", "3.12", filepath.Join(home(), "rkb-py")) != nil ||
		run(uv, "pip", "install", "--python", pyVenv(), "numpy", torch) != nil {
		return
	}
	_ = run(uv, "cache", "clean") // frees disk only; the environment is ready either way
	if pythonReady(pyVenv()) {
		fmt.Println("python: ~/rkb-py ready (numpy, torch); `rkb claims vm` passes it as VALIDATE_PYTHON")
	}
}

func cmdSetup(a *args) {
	if _, err := exec.LookPath("go"); err == nil && !a.bools["force"] {
		fmt.Println("go: already on PATH")
	} else if dirExists(filepath.Join(home(), ".local", "go", "bin")) && !a.bools["force"] {
		fmt.Println("go: ~/.local/go present")
	} else {
		installGo(a.get("go-version", ""))
	}
	var sums []string
	root := codeRoot()
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (skipDirs[d.Name()] || strings.Count(strings.TrimPrefix(p, root), string(os.PathSeparator)) > 4) {
			return filepath.SkipDir
		}
		if d.Name() == "go.sum" {
			sums = append(sums, p)
		}
		return nil
	})
	if err := buildGoProxy(filepath.Join(home(), "goproxy"), sums); err != nil {
		fmt.Println("goproxy:", err)
	}
	installPython()
	for _, t := range []string{"pdftotext", "pdfinfo"} {
		if _, err := exec.LookPath(t); err != nil {
			fmt.Printf("%s: missing — install poppler (macOS: brew install poppler)\n", t)
		}
	}
	if _, err := exec.LookPath("soffice"); err != nil {
		fmt.Println("soffice: not found — Word page counts will be skipped (optional)")
	}
	// chromePath, not findChrome: findChrome exits the process when Chrome is
	// missing, and setup reports a missing tool instead of stopping.
	if p, err := chromePath(); err != nil {
		fmt.Println("chrome:", err)
	} else {
		fmt.Println("chrome:", p)
	}
	fmt.Println("environment for checks:", strings.Join(goEnv(), " "))
}

func installGo(version string) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get("https://raw.githubusercontent.com/actions/go-versions/main/versions-manifest.json")
	if err != nil {
		die("go manifest: %v", err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only body: a close error changes nothing
	if resp.StatusCode != http.StatusOK {
		die("go manifest: HTTP %d", resp.StatusCode)
	}
	var man []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
		Files   []struct {
			P string `json:"platform"`
			A string `json:"arch"`
			U string `json:"download_url"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&man); err != nil {
		die("go manifest: %v", err)
	}
	arch := runtime.GOARCH
	for _, v := range man {
		if !v.Stable || (version != "" && v.Version != version) {
			continue
		}
		for _, f := range v.Files {
			if f.P == runtime.GOOS && f.A == arch {
				dest := filepath.Join(home(), ".local", "go")
				if err := os.MkdirAll(dest, 0o755); err != nil {
					die("go install dir: %v", err)
				}
				tgz := filepath.Join(os.TempDir(), "go.tgz")
				if out, err := exec.Command("curl", "-sL", "--max-time", "300", "-o", tgz, f.U).CombinedOutput(); err != nil {
					die("download %s: %v %s", f.U, err, out)
				}
				if out, err := exec.Command("tar", "xzf", tgz, "-C", dest).CombinedOutput(); err != nil {
					die("extract: %v %s", err, out)
				}
				_ = os.Remove(tgz) // the archive is already extracted; a leftover temp file is harmless
				fmt.Printf("go %s installed in %s\n", v.Version, dest)
				return
			}
		}
	}
	die("no Go %s build for %s/%s in the manifest", version, runtime.GOOS, arch)
}

var reSumLine = regexp.MustCompile(`^(golang\.org/x/[\w.-]+)\s+(v[^\s/]+)(/go\.mod)?\s+h1:`)

func buildGoProxy(out string, sums []string) error {
	need := map[[2]string]bool{}
	for _, p := range sums {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := reSumLine.FindStringSubmatch(sc.Text()); m != nil {
				k := [2]string{m[1], m[2]}
				need[k] = need[k] || m[3] == ""
			}
		}
		scanErr := sc.Err()
		_ = f.Close() // read-only
		if scanErr != nil {
			return fmt.Errorf("read %s: %w", p, scanErr)
		}
	}
	work := filepath.Join(os.TempDir(), "go-mirror-src")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	ok, bad := 0, 0
	keysSorted := make([][2]string, 0, len(need))
	for k := range need {
		keysSorted = append(keysSorted, k)
	}
	sort.Slice(keysSorted, func(i, j int) bool { return keysSorted[i][0]+keysSorted[i][1] < keysSorted[j][0]+keysSorted[j][1] })
	for _, k := range keysSorted {
		mod, ver, full := k[0], k[1], need[k]
		ext := ".mod"
		if full {
			ext = ".zip"
		}
		if fileExists(filepath.Join(out, mod, "@v", ver+ext)) {
			ok++
			continue
		}
		name := strings.Split(mod, "/")[2]
		src := filepath.Join(work, name+"@"+ver)
		if !dirExists(src) {
			ref := ver
			if m := regexp.MustCompile(`-(\d{14})-([0-9a-f]{12})$`).FindStringSubmatch(ver); m != nil {
				ref = m[2]
			}
			url := "https://github.com/golang/" + name
			var err error
			if ref == ver {
				err = exec.Command("git", "clone", "-q", "--depth", "1", "--branch", ver, url, src).Run()
			} else if err = exec.Command("git", "clone", "-q", "--filter=blob:none", url, src).Run(); err == nil {
				err = exec.Command("git", "-C", src, "checkout", "-q", ref).Run()
			}
			if err != nil {
				bad++
				fmt.Printf("FAIL %s@%s: %v\n", mod, ver, err)
				continue
			}
		}
		if err := writeProxyModule(out, mod, ver, src, full); err != nil {
			bad++
			fmt.Printf("FAIL %s@%s: %v\n", mod, ver, err)
			continue
		}
		ok++
	}
	fmt.Printf("goproxy: %d module versions ready in %s, %d failed\n", ok, out, bad)
	if bad > 0 {
		return fmt.Errorf("%d modules failed", bad)
	}
	return nil
}

// writeProxyModule follows Go's module zip rules: skip .git, skip nested
// modules (subdirectories with their own go.mod), skip symlinks.
func writeProxyModule(out, mod, ver, src string, full bool) error {
	base := filepath.Join(out, mod, "@v")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	gm, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, ver+".mod"), gm, 0o644); err != nil {
		return err
	}
	// The commit time is metadata only: without git (or a commit) "now" stands in.
	t, _ := exec.Command("git", "-C", src, "log", "-1", "--format=%cI").Output()
	ts := strings.TrimSpace(string(t))
	if ts == "" {
		ts = time.Now().Format(time.RFC3339)
	}
	info, err := json.Marshal(map[string]string{"Version": ver, "Time": ts})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, ver+".info"), info, 0o644); err != nil {
		return err
	}
	if full {
		if err := writeModuleZip(filepath.Join(base, ver+".zip"), mod, ver, src); err != nil {
			return err
		}
	}
	lst := filepath.Join(base, "list")
	have := map[string]bool{ver: true}
	if b, err := os.ReadFile(lst); err == nil {
		for _, v := range strings.Fields(string(b)) {
			have[v] = true
		}
	}
	var vs []string
	for v := range have {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	return os.WriteFile(lst, []byte(strings.Join(vs, "\n")+"\n"), 0o644)
}

// writeModuleZip writes src as the module zip for mod@ver. Any walk, read or
// write error fails it, and a failed zip is removed, so the proxy never serves
// a zip with files silently missing.
func writeModuleZip(path, mod, ver, src string) (err error) {
	var files []string
	if err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || (p != src && fileExists(filepath.Join(p, "go.mod"))) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		files = append(files, p)
		return nil
	}); err != nil {
		return fmt.Errorf("walk %s: %w", src, err)
	}
	sort.Strings(files)
	zf, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = zf.Close()      // already failing; the write error is the one to report
			_ = os.Remove(path) // a partial zip must not be served
		}
	}()
	z := zip.NewWriter(zf)
	for _, p := range files {
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		w, err := z.Create(mod + "@" + ver + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	if err := z.Close(); err != nil {
		return err
	}
	return zf.Close()
}
