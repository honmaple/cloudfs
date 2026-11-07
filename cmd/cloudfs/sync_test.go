package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/syncer"
)

func TestSyncPreview(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			config, left, right := namedPair(t)
			state := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(filepath.Join(left, "a"), []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(right, "b"), []byte("target"), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := cliRun(config, mode, "--dry-run", "--state", state, "left:/", "right:/")
			if err != nil || !strings.Contains(out, "copy target /a\n") {
				t.Fatalf("preview: %q, %v", out, err)
			}
			if mode == "bisync" && !strings.Contains(out, "copy source /b\n") {
				t.Fatalf("reverse preview: %q", out)
			}
			for _, path := range []string{state, filepath.Join(right, "a"), filepath.Join(left, "b")} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("preview created %s: %v", path, err)
				}
			}
		})
	}
}

func TestSyncDeleteGuard(t *testing.T) {
	config, left, right := namedPair(t)
	if err := os.WriteFile(filepath.Join(left, "a"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(right, "tree"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "tree", "b"), []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range [][]string{{"--max-delete", "1"}, {"--max-delete-percent", "99"}} {
		args := append([]string{"sync", "--delete"}, limit...)
		args = append(args, "left:/", "right:/")
		if _, err := cliRun(config, args...); !errors.Is(err, syncer.ErrTooManyDeletes) {
			t.Fatalf("%v: %v", limit, err)
		}
		if _, err := os.Stat(filepath.Join(right, "a")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("guard did not prevent an earlier copy")
		}
	}
}

func TestSyncChecksumWithState(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			config, left, right := namedPair(t)
			state := filepath.Join(t.TempDir(), "state.json")
			for side, root := range []string{left, right} {
				file := filepath.Join(root, "file")
				if err := os.WriteFile(file, []byte("same contents"), 0600); err != nil {
					t.Fatal(err)
				}
				stamp := time.Date(2000+side, 1, 1, 0, 0, 0, 0, time.UTC)
				if err := os.Chtimes(file, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if _, err := cliRun(config, mode, "--checksum", "--state", state, "left:/", "right:/"); err != nil {
					t.Fatal(err)
				}
			}
			for side, root := range []string{left, right} {
				info, err := os.Stat(filepath.Join(root, "file"))
				stamp := time.Date(2000+side, 1, 1, 0, 0, 0, 0, time.UTC)
				if err != nil || !info.ModTime().Equal(stamp) {
					t.Fatalf("identical content was rewritten: %v", err)
				}
			}
		})
	}
}

func TestBiSyncKeepBoth(t *testing.T) {
	config, left, right := namedPair(t)
	state := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(filepath.Join(left, "file"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "file"), []byte("target version"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"bisync", "--conflict", "both", "--state", state}
	if _, err := cliRun(config, "sync", "--conflict", "both", "left:/", "right:/"); !errors.Is(err, syncer.ErrSyncConflict) {
		t.Fatalf("one-way keep-both: %v", err)
	}
	preview := append(append([]string(nil), args...), "--dry-run", "left:/", "right:/")
	if out, err := cliRun(config, preview...); err != nil || !strings.Contains(out, "copy source /file.conflict") || !strings.Contains(out, "rename target /file -> /file.conflict") {
		t.Fatalf("preview: %q, %v", out, err)
	}
	if _, err := cliRun(config, append(args, "left:/", "right:/")...); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{left, right} {
		data, err := os.ReadFile(filepath.Join(root, "file.conflict"))
		if err != nil || string(data) != "target version" {
			t.Fatalf("backup: %q, %v", data, err)
		}
	}
}

func TestSyncConcurrency(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			config, left, right := namedPair(t)
			state := filepath.Join(t.TempDir(), "state.json")
			for _, name := range []string{"a", "b"} {
				if err := os.WriteFile(filepath.Join(left, name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := cliRun(config, mode, "--concurrency", "-1", "--state", state, "left:/", "right:/"); err == nil {
				t.Fatal("accepted negative concurrency")
			}
			if _, err := cliRun(config, mode, "--concurrency", "2", "--state", state, "left:/", "right:/"); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a", "b"} {
				data, err := os.ReadFile(filepath.Join(right, name))
				if err != nil || string(data) != name {
					t.Fatalf("%s: %q, %v", name, data, err)
				}
			}
		})
	}
}

func TestSyncPreferNewer(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			config, left, right := namedPair(t)
			state := filepath.Join(t.TempDir(), "state.json")
			for side, root := range []string{left, right} {
				content := "source"
				if side == 1 {
					content = "newer target version"
				}
				if err := os.WriteFile(filepath.Join(root, "file"), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				stamp := time.Date(2000+side, 1, 1, 0, 0, 0, 0, time.UTC)
				if err := os.Chtimes(filepath.Join(root, "file"), stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := cliRun(config, mode, "--conflict", "newer", "--state", state, "left:/", "right:/"); err != nil {
				t.Fatal(err)
			}
			for side, root := range []string{left, right} {
				want := "newer target version"
				if mode == "sync" && side == 0 {
					want = "source"
				}
				data, err := os.ReadFile(filepath.Join(root, "file"))
				if err != nil || string(data) != want {
					t.Fatalf("got %q, %v; want %q", data, err, want)
				}
			}
		})
	}
}
