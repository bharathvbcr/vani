// Package localserver starts a local decision-model server (llama.cpp's
// llama-server) for the length of one run and stops it afterwards, so the
// daily run can ask a model on this Mac without a server left running.
//
// The server listens on 127.0.0.1 only, gets a minimal environment, and is
// waited on through its /health endpoint for a bounded time. A server that
// does not come up is stopped and reported; the caller runs without it.
package localserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/childenv"
	"github.com/bharathvbcr/Vani/internal/egress"
)

const (
	DefaultContextSize  = 8192
	MaxContextSize      = 65536
	DefaultStartTimeout = 90 * time.Second
	MaxStartTimeout     = 5 * time.Minute
	stopGrace           = 10 * time.Second
	maxStderr           = 64 << 10
	stderrInError       = 400
	// Path is llama.cpp's decision endpoint.
	Path = "/v1/systemone"
)

// Config says what to run. Binary and Model are absolute paths.
type Config struct {
	Binary       string
	Model        string
	ContextSize  int           // zero means DefaultContextSize
	StartTimeout time.Duration // zero means DefaultStartTimeout
	// BatchSize sets llama.cpp's logical and physical batch (-b, -ub). Zero
	// keeps llama.cpp's default. Decision models that read the whole prompt in
	// one batch (vllm-d3) need it at least as large as the longest prompt.
	BatchSize int
	// Parallel sets -np. Zero keeps llama.cpp's auto (unified KV pool); an
	// explicit value splits ContextSize across that many slots.
	Parallel int
}

// MaxParallel bounds Config.Parallel.
const MaxParallel = 16

// Server is a running server. Endpoint is its decision URL.
type Server struct {
	Endpoint string

	cmd    *exec.Cmd
	exited chan struct{}
	stderr *capped
	once   sync.Once
}

// args is the llama-server command line for a validated cfg.
// Metal only, every layer: CPU is never a fallback. Without MTL0 llama.cpp
// exits at once ("invalid device"), so the run goes on without the shadow
// rather than measuring a different numeric path. -np stays on auto unless
// Parallel is set, which keeps the KV pool unified (an explicit -np splits -c
// across slots).
func args(cfg Config, port int) []string {
	a := []string{"-m", cfg.Model, "--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"-c", strconv.Itoa(cfg.ContextSize), "--device", "MTL0", "-ngl", "all"}
	if cfg.BatchSize > 0 {
		a = append(a, "-b", strconv.Itoa(cfg.BatchSize), "-ub", strconv.Itoa(cfg.BatchSize))
	}
	if cfg.Parallel > 0 {
		a = append(a, "-np", strconv.Itoa(cfg.Parallel))
	}
	return a
}

// Start launches the server and returns once /health answers ok. On any
// failure the process is stopped before Start returns.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	if !filepath.IsAbs(cfg.Binary) || !filepath.IsAbs(cfg.Model) {
		return nil, errors.New("localserver: binary and model must be absolute paths")
	}
	if _, err := os.Stat(cfg.Model); err != nil {
		return nil, fmt.Errorf("localserver: model: %w", err)
	}
	switch {
	case cfg.ContextSize == 0:
		cfg.ContextSize = DefaultContextSize
	case cfg.ContextSize < 512 || cfg.ContextSize > MaxContextSize:
		return nil, fmt.Errorf("localserver: context size %d outside 512..%d", cfg.ContextSize, MaxContextSize)
	}
	switch {
	case cfg.StartTimeout == 0:
		cfg.StartTimeout = DefaultStartTimeout
	case cfg.StartTimeout < 0 || cfg.StartTimeout > MaxStartTimeout:
		return nil, fmt.Errorf("localserver: start timeout %s outside (0, %s]", cfg.StartTimeout, MaxStartTimeout)
	}
	if cfg.BatchSize != 0 && (cfg.BatchSize < 32 || cfg.BatchSize > cfg.ContextSize) {
		return nil, fmt.Errorf("localserver: batch size %d outside 32..%d (the context size)", cfg.BatchSize, cfg.ContextSize)
	}
	if cfg.Parallel < 0 || cfg.Parallel > MaxParallel {
		return nil, fmt.Errorf("localserver: parallel %d outside 0..%d", cfg.Parallel, MaxParallel)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	// Not exec.CommandContext: the server must outlive Start's context and be
	// stopped by Stop, gracefully.
	cmd := exec.Command(cfg.Binary, args(cfg, port)...)
	cmd.Env = childenv.Minimal() // no API keys or other secrets reach the server
	cmd.Stdout = io.Discard
	s := &Server{Endpoint: base + Path, cmd: cmd, exited: make(chan struct{}), stderr: &capped{limit: maxStderr}}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("localserver: start: %w", err)
	}
	go func() { _ = cmd.Wait(); close(s.exited) }()

	if err := s.waitHealthy(ctx, base+"/health", cfg.StartTimeout); err != nil {
		s.Stop()
		return nil, fmt.Errorf("%w%s", err, s.stderrTail())
	}
	return s, nil
}

func (s *Server) waitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	d, err := egress.NewLoopback(5*time.Second, 4096)
	if err != nil {
		return err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		if ok := probe(ctx, d, url); ok {
			return nil
		}
		select {
		case <-s.exited:
			return errors.New("localserver: the server exited before it was ready")
		case <-deadline.C:
			return fmt.Errorf("localserver: not ready after %s", timeout)
		case <-ctx.Done():
			return fmt.Errorf("localserver: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

func probe(ctx context.Context, d *egress.Loopback, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := d.Do(ctx, req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return err == nil && resp.StatusCode == http.StatusOK && strings.Contains(string(b), `"ok"`)
}

// Stop asks the server to exit, and kills it after a grace period. Safe to
// call more than once.
func (s *Server) Stop() {
	s.once.Do(func() {
		if s.cmd.Process == nil {
			return
		}
		_ = terminate(s.cmd.Process)
		select {
		case <-s.exited:
		case <-time.After(stopGrace):
			_ = s.cmd.Process.Kill()
			<-s.exited
		}
	})
}

// Exited reports whether the server process has ended.
func (s *Server) Exited() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

func (s *Server) stderrTail() string {
	t := strings.TrimSpace(s.stderr.String())
	if t == "" {
		return ""
	}
	if len(t) > stderrInError {
		t = "…" + t[len(t)-stderrInError:]
	}
	return ": " + t
}

// freePort asks the kernel for an unused loopback port. Another process could
// take it before the server binds; the server then fails to start and the
// run carries on without it.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("localserver: no free port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// capped keeps the last limit bytes written: the end of stderr is where a
// load error is.
type capped struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.buf.Write(p)
	if over := c.buf.Len() - c.limit; over > 0 {
		c.buf.Next(over)
	}
	return n, nil
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
