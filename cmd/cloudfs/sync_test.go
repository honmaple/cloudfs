package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/syncer"
)

func TestSyncLogs(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			config, left, right := namedPair(t)
			state := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(filepath.Join(left, "file"), []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(right, "reverse"), []byte("target"), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"cloudfs", "--config", config, mode, "--state", state, "left:/", "right:/"}
			if err := run(context.Background(), args, nil, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %q", stderr.String())
			}
			out := stdout.String()
			for _, line := range []string{
				"phase=scan status=started",
				`phase=execute status=succeeded op=copy path="file" reverse=false`,
			} {
				if !strings.Contains(out, line) {
					t.Fatalf("missing %q in stdout: %s", line, out)
				}
			}
			for _, phase := range []string{"scan", "plan", "execute", "finish"} {
				prefix := "phase=" + phase + " status=succeeded elapsed="
				_, timing, found := strings.Cut(out, prefix)
				timing, _, _ = strings.Cut(timing, "\n")
				elapsed, err := time.ParseDuration(timing)
				if !found || err != nil || elapsed < 0 {
					t.Fatalf("missing valid duration for %s: %s", phase, out)
				}
			}
			if strings.Contains(out, "phase=finish status=started") {
				t.Fatalf("unexpected finish start in stdout: %s", out)
			}
			if mode == "bisync" && !strings.Contains(out, `phase=execute status=succeeded op=copy path="reverse" reverse=true`) {
				t.Fatalf("missing reverse copy in stdout: %s", out)
			}
		})
	}
}

func TestSyncPreview(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			config, left, right := namedPair(t)
			state := filepath.Join(t.TempDir(), "state.json")
			args := []string{"cloudfs", mode, "left:/", "right:/", "--dry-run", "--state", state, "--config", config}
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), args, nil, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if out := stdout.String(); !strings.Contains(out, "phase=plan status=succeeded") || strings.Contains(out, "phase=execute") || strings.Contains(out, "status=planned") || stderr.Len() != 0 {
				t.Fatalf("empty preview: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			stdout.Reset()
			if err := os.WriteFile(filepath.Join(left, "a"), []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(right, "b"), []byte("target"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := run(context.Background(), args, nil, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			out := stdout.String()
			if !strings.Contains(out, `phase=execute status=planned op=copy path="a" reverse=false`) {
				t.Fatalf("preview: %q", out)
			}
			if mode == "bisync" && !strings.Contains(out, `phase=execute status=planned op=copy path="b" reverse=true`) {
				t.Fatalf("reverse preview: %q", out)
			}
			if !strings.Contains(out, "phase=plan status=started") || !strings.Contains(out, "phase=execute status=succeeded") || stderr.Len() != 0 {
				t.Fatalf("preview status: stdout=%q stderr=%q", out, stderr.String())
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
	for _, limit := range [][]string{{"--max-delete", "0"}, {"--max-delete", "1"}, {"--max-delete-percent", "99"}} {
		args := append([]string{"sync", "--mirror"}, limit...)
		args = append(args, "left:/", "right:/")
		out, err := cliRun(config, args...)
		if !errors.Is(err, syncer.ErrTooManyDeletes) {
			t.Fatalf("%v: %v", limit, err)
		}
		_, failure, found := strings.Cut(out, "phase=plan status=failed ")
		if !found || !strings.Contains(failure, " elapsed=") {
			t.Fatalf("missing duration for planning failure: %q", out)
		}
		if _, err := os.Stat(filepath.Join(right, "a")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("guard did not prevent an earlier copy")
		}
	}
	if _, err := cliRun(config, "sync", "--mirror", "left:/", "right:/"); err != nil {
		t.Fatalf("mirror without a deletion limit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(right, "tree")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target-only tree was not deleted: %v", err)
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
	if out, err := cliRun(config, preview...); err != nil || !strings.Contains(out, `phase=execute status=planned op=copy path="file.conflict" reverse=true`) || !strings.Contains(out, `phase=execute status=planned op=rename path="file" reverse=false new_path="file.conflict"`) {
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
