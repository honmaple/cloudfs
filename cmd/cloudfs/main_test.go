package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/honmaple/cloudfs"
)

func TestLocalCommands(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	invoke := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(context.Background(), append([]string{"cloudfs"}, args...), nil, &out, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}
	if err := os.WriteFile("input", []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	invoke("mkdir", "docs")
	invoke("cp", "./input", "docs/file")
	if got := invoke("cat", filepath.Join(root, "docs/file")); got != "hello" {
		t.Fatalf("cat=%q", got)
	}
	invoke("cp", "docs/file", "docs/uploaded")
	if !strings.Contains(invoke("ls"), "docs") {
		t.Fatal("default listing is not current directory")
	}
	var info struct {
		Size int64 `json:"size"`
	}
	if err := json.Unmarshal([]byte(invoke("stat", "docs/file")), &info); err != nil || info.Size != 5 {
		t.Fatalf("stat=%+v %v", info, err)
	}
	invoke("mkdir", "copies")
	invoke("cp", "docs/file", "copies/file")
	invoke("rename", "copies/file", "renamed")
	invoke("mv", "copies/renamed", "docs/renamed")
	if got := invoke("cat", "docs/renamed"); got != "hello" {
		t.Fatal(got)
	}
	invoke("rm", "docs/renamed")
	invoke("rm", "-r", "copies")
	invoke("rm", "--recursive", "docs")
	if _, err := os.Stat("docs"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestHelpAndValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"help", "cp"}, {"rm", "--help"}} {
		var out bytes.Buffer
		if err := run(context.Background(), append([]string{filepath.Join("bin", "cloudfs-custom")}, args...), nil, &out, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "cloudfs-custom") {
			t.Fatalf("help does not use the executable name: %s", &out)
		}
	}
	for _, args := range [][]string{{"copy", "a", "b"}, {"move", "a", "b"}, {"put", "a", "b"}, {"get", "a", "b"}, {"unknown"}, {"rename", "a", "../b"}, {"--root", ".", "ls"}} {
		var out bytes.Buffer
		if err := run(context.Background(), append([]string{"cloudfs"}, args...), nil, &out, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "dir"), 0755)
	for _, args := range [][]string{{"rm", filepath.Join(root, "dir")}, {"rm", "-r", "/"}} {
		var out bytes.Buffer
		if err := run(context.Background(), append([]string{"cloudfs"}, args...), nil, &out, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestConfiguredDriver(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "file"), []byte("configured"), 0600)
	config := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(map[string]any{"named": map[string]any{"driver": "local", "option": map[string]any{"path": root}}})
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"cloudfs", "--config", config, "cat", "named:/file"}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "configured" {
		t.Fatal(out.String())
	}
	want, err := cliRun(config, "ls", "named:/")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cliRun(config, "ls", "named:"); err != nil || got != want {
		t.Fatalf("ls named: = %q, %v; want %q", got, err, want)
	}
}

type failingWriterFS struct {
	cloudfs.BaseFS
	closed bool
}
type failingWriter struct {
	owner *failingWriterFS
}

var errUpload = errors.New("upload close failed")

func (f *failingWriterFS) Create(context.Context, string) (cloudfs.FileWriter, error) {
	return &failingWriter{
		owner: f,
	}, nil
}
func (w *failingWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *failingWriter) Close() error                { w.owner.closed = true; return errUpload }
func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := run(ctx, []string{"cloudfs", "mkdir", "dir"}, nil, &out, &out); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
