package kilden

// Test harness for the spec mock server: builds the binary from the spec
// checkout once per test run and starts one instance shared by the tests
// that need it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"
)

var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

type mock struct {
	url string
}

var (
	mockOnce sync.Once
	mockInst *mock
	mockErr  error
	mockCmd  *exec.Cmd
)

// TestMain kills the shared mock server process; without it `go test`
// waits a full WaitDelay for the child's inherited stdio to close.
func TestMain(m *testing.M) {
	code := m.Run()
	if mockCmd != nil && mockCmd.Process != nil {
		mockCmd.Process.Kill()
	}
	os.Exit(code)
}

func mockServer(t *testing.T) *mock {
	t.Helper()
	dir := specDir(t)
	mockOnce.Do(func() { mockInst, mockErr = startMock(dir) })
	if mockErr != nil {
		t.Fatalf("mock server: %v", mockErr)
	}
	return mockInst
}

func startMock(dir string) (*mock, error) {
	bin := filepath.Join(os.TempDir(), fmt.Sprintf("kilden-mockserver-%d", os.Getpid()))
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(dir, "mockserver")
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building mockserver: %v\n%s", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().String()
	ln.Close()

	cmd := exec.Command(bin, "-addr", addr)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	mockCmd = cmd

	url := "http://" + addr
	for range 50 {
		resp, err := http.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			return &mock{url: url}, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	cmd.Process.Kill()
	return nil, fmt.Errorf("mock server did not become healthy at %s", url)
}

func (m *mock) reset(t *testing.T) {
	t.Helper()
	m.post(t, "/__mock/reset", "{}")
}

func (m *mock) post(t *testing.T, path, body string) {
	t.Helper()
	resp, err := http.Post(m.url+path, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %d", path, resp.StatusCode)
	}
}

func (m *mock) captured(t *testing.T) []map[string]any {
	t.Helper()
	resp, err := http.Get(m.url + "/__mock/captured")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc.Events
}
