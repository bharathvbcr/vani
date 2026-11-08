package jarvisio

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fakePath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakejarvis")
	if err != nil {
		panic(err)
	}
	fakePath = filepath.Join(dir, "jarvis")
	build := exec.Command("go", "build", "-o", fakePath, "./testdata/fake")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic("build fake jarvis: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func client(t *testing.T, mode string) Client {
	t.Helper()
	if fakePath == "" {
		t.Fatal("fake jarvis was not built")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	return Client{Binary: fakePath, Root: root, Timeout: 3 * time.Second}
}

func TestPutThenListKeysFailsWhenTheBankIsMissing(t *testing.T) {
	c := client(t, "bank-missing")
	const secret = "contact-secret-example-9f3a"
	if err := c.Put(context.Background(), "email", secret); err != nil {
		t.Fatal(err)
	}
	keys, err := c.List(context.Background())
	if !BankMissing(err) {
		t.Fatalf("list = %v, %v; a missing bank must not look like an empty catalog", keys, err)
	}
	if keys != nil {
		t.Fatalf("list returned keys on failure: %v", keys)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("list error contains the stored value")
	}
	text, err := c.Get(context.Background(), "email")
	if !BankMissing(err) || text != "" {
		t.Fatalf("get = %q, %v", text, err)
	}
}

func TestServeDownIsNotAnEmptyCatalog(t *testing.T) {
	c := client(t, "ok")
	c.Binary = filepath.Join(t.TempDir(), "missing-jarvis")
	keys, err := c.List(context.Background())
	if !Unavailable(err) || keys != nil {
		t.Fatalf("list = %v, %v", keys, err)
	}
	if err := c.Put(context.Background(), "email", "x"); err == nil || !Unavailable(err) {
		t.Fatalf("put = %v", err)
	}
}

func TestServeThatExitsIsUnavailable(t *testing.T) {
	c := client(t, "down")
	keys, err := c.List(context.Background())
	if !Unavailable(err) || keys != nil {
		t.Fatalf("list = %v, %v", keys, err)
	}
}

func TestBadRequestIsNotStored(t *testing.T) {
	c := client(t, "bad-request")
	err := c.Put(context.Background(), "email", "x")
	var je *Error
	if err == nil || !errorsAs(err, &je) || je.Code != CodeBadRequest {
		t.Fatalf("put = %v", err)
	}
}

func TestRefusedInputDoesNotEchoTheValue(t *testing.T) {
	c := client(t, "echo-secret")
	const secret = "contact-secret-example-9f3a"
	err := c.Put(context.Background(), "email", secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoes the value: %v", err)
	}
	var je *Error
	if !errorsAs(err, &je) || je.Code != CodeJarvis {
		t.Fatalf("code = %v", err)
	}
}

func TestListAndGetRoundTrip(t *testing.T) {
	c := client(t, "ok")
	keys, err := c.List(context.Background())
	if err != nil || len(keys) != 1 || keys[0] != "email" {
		t.Fatalf("list = %v, %v", keys, err)
	}
	text, err := c.Get(context.Background(), "email")
	if err != nil || text != "stored-example" {
		t.Fatalf("get = %q, %v", text, err)
	}
}

func TestTimeoutDoesNotLookLikeAnEmptyList(t *testing.T) {
	c := client(t, "sleep")
	c.Timeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keys, err := c.List(ctx)
	if err == nil || keys != nil || BankMissing(err) {
		t.Fatalf("silent serve list = %v, %v", keys, err)
	}
}

func errorsAs(err error, target **Error) bool {
	return errors.As(err, target)
}
