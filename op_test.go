package cloudfs_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver/local"
)

func TestCopyBetween(t *testing.T) {
	sourceDir, targetDir := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(sourceDir, "tree/empty"), 0755)
	os.WriteFile(filepath.Join(sourceDir, "tree/file"), []byte("copied"), 0600)
	os.MkdirAll(filepath.Join(targetDir, "renamed"), 0755)
	os.WriteFile(filepath.Join(targetDir, "renamed/keep"), []byte("preserved"), 0600)
	source, err := local.New(&local.Option{Path: sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := local.New(&local.Option{Path: targetDir})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := cloudfs.CopyBetween(context.Background(), source, "/tree/", target, "/renamed"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"renamed/file", "renamed/keep", "renamed/empty"} {
		if _, err := os.Stat(filepath.Join(targetDir, file)); err != nil {
			t.Fatal(err)
		}
	}
	if err := cloudfs.CopyBetween(context.Background(), source, "/tree/file", target, "/different-name"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(targetDir, "different-name")); err != nil || string(data) != "copied" {
		t.Fatalf("%q %v", data, err)
	}
}

type copyTestFS struct {
	cloudfs.BaseFS
	readErr, createErr, closeErr error
	closes, stats, creates       int
	mode                         fs.FileMode
}

func (f *copyTestFS) Stat(context.Context, string) (cloudfs.FileInfo, error) {
	f.stats++
	return (&cloudfs.Entry{Name: "file", Mode: f.mode}).FileInfo(), nil
}
func (f *copyTestFS) Open(context.Context, string) (cloudfs.File, error) {
	return &copyTestReader{Reader: strings.NewReader("content"), owner: f}, nil
}
func (f *copyTestFS) Create(context.Context, string) (cloudfs.FileWriter, error) {
	f.creates++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &copyTestWriter{owner: f}, nil
}

type copyTestReader struct {
	*strings.Reader
	owner *copyTestFS
}

func (r *copyTestReader) Read(p []byte) (int, error) {
	if r.owner.readErr != nil {
		return 0, r.owner.readErr
	}
	return r.Reader.Read(p)
}
func (r *copyTestReader) Close() error { r.owner.closes++; return r.owner.closeErr }

type copyTestWriter struct{ owner *copyTestFS }

func (w *copyTestWriter) Write(p []byte) (int, error) { return io.Discard.Write(p) }
func (w *copyTestWriter) Close() error                { w.owner.closes++; return w.owner.closeErr }

func TestCopyBetweenFailureClosesStreams(t *testing.T) {
	transfer, sourceClose, targetClose := errors.New("transfer"), errors.New("source close"), errors.New("target close")
	source := &copyTestFS{readErr: transfer, closeErr: sourceClose}
	target := &copyTestFS{closeErr: targetClose}
	err := cloudfs.CopyBetween(context.Background(), source, "/a", target, "/b")
	for _, cause := range []error{transfer, sourceClose, targetClose} {
		if !errors.Is(err, cause) {
			t.Fatalf("lost %v: %v", cause, err)
		}
	}
	if source.closes != 1 || target.closes != 1 {
		t.Fatalf("close counts %d %d", source.closes, target.closes)
	}
	source = &copyTestFS{closeErr: sourceClose}
	target = &copyTestFS{createErr: targetClose}
	err = cloudfs.CopyBetween(context.Background(), source, "/a", target, "/b")
	if !errors.Is(err, sourceClose) || !errors.Is(err, targetClose) || source.closes != 1 {
		t.Fatalf("create failure: %v closes=%d", err, source.closes)
	}
}

func TestCopyBetweenCancellationAndSpecialFile(t *testing.T) {
	source, target := &copyTestFS{}, &copyTestFS{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cloudfs.CopyBetween(ctx, source, "/a", target, "/b"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if source.stats != 0 || target.creates != 0 {
		t.Fatal("canceled copy performed I/O")
	}
	source.mode = fs.ModeSymlink
	if err := cloudfs.CopyBetween(context.Background(), source, "/a", target, "/b"); err == nil {
		t.Fatal("accepted symbolic link")
	}
	if target.creates != 0 {
		t.Fatal("special file created target")
	}
}
