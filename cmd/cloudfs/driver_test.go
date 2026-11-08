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

func namedPair(t *testing.T) (string, string, string) {
	t.Helper()
	left, right := t.TempDir(), t.TempDir()
	config := filepath.Join(t.TempDir(), "drivers.json")
	data, _ := json.Marshal(map[string]any{
		"left":  map[string]any{"driver": "local", "option": map[string]string{"path": left}},
		"right": map[string]any{"driver": "local", "option": map[string]string{"path": right}},
	})
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	return config, left, right
}
func cliRun(config string, args ...string) (string, error) {
	var out bytes.Buffer
	err := run(context.Background(), append([]string{"cloudfs", "--config", config}, args...), strings.NewReader(""), &out, &out)
	return out.String(), err
}
func TestNamedCopyMove(t *testing.T) {
	config, left, right := namedPair(t)
	os.WriteFile(filepath.Join(left, "a"), []byte("data"), 0600)
	os.Mkdir(filepath.Join(right, "share"), 0755)
	if _, err := cliRun(config, "cp", "left:/a", "right:/share/b"); err != nil {
		t.Fatal(err)
	}
	if out, err := cliRun(config, "cat", "right:/share/b"); err != nil || out != "data" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := cliRun(config, "mv", "right:/share/b", "left:/moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(right, "share/b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(left, "tree/empty"), 0755)
	os.WriteFile(filepath.Join(left, "tree/file"), []byte("child"), 0600)
	if _, err := cliRun(config, "cp", "left:/tree", "right:/renamed-tree"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(right, "renamed-tree/file")); err != nil || string(data) != "child" {
		t.Fatalf("%q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(right, "renamed-tree/empty")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"cp", "left:/a", "left:/a"}, {"cp", "left:/tree", "left:/tree/nested"}, {"cp", "unknown:/a", "right:/b"}} {
		if _, err := cliRun(config, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestSyncCommands(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			config, left, right := namedPair(t)
			state := filepath.Join(t.TempDir(), "state.json")
			os.Mkdir(filepath.Join(left, "docs"), 0755)
			os.Mkdir(filepath.Join(right, "backup"), 0755)
			os.WriteFile(filepath.Join(left, "docs/a"), []byte("one"), 0600)
			args := []string{mode, "--state", state, "left:/docs", "right:/backup"}
			if _, err := cliRun(config, args...); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(state)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), left) || strings.Contains(string(data), right) {
				t.Fatal("raw configuration in state")
			}
			if mode == "bisync" {
				os.WriteFile(filepath.Join(right, "backup/new"), []byte("reverse"), 0600)
				if _, err := cliRun(config, args...); err != nil {
					t.Fatal(err)
				}
				if data, err := os.ReadFile(filepath.Join(left, "docs/new")); err != nil || string(data) != "reverse" {
					t.Fatalf("%q %v", data, err)
				}
			} else {
				os.WriteFile(filepath.Join(right, "backup/extra"), []byte("extra"), 0600)
				if _, err := cliRun(config, "sync", "--state", state, "--mirror", "left:/docs", "right:/backup"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(right, "backup/extra")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			if _, err := cliRun(config, mode, "--state", state, "right:/backup", "left:/docs"); err == nil {
				t.Fatal("accepted reversed state binding")
			}
		})
	}
	config, _, _ := namedPair(t)
	if _, err := cliRun(config, "bisync", "left:/", "right:/"); err == nil {
		t.Fatal("accepted missing state")
	}
	if _, err := cliRun(config, "sync", "--conflict", "invalid", "left:/", "right:/"); err == nil {
		t.Fatal("accepted invalid policy")
	}
}

type copySource struct {
	cloudfs.BaseFS
	removed bool
}

func (s *copySource) Stat(context.Context, string) (cloudfs.FileInfo, error) {
	return (&cloudfs.Entry{
		Name: "file",
		Size: 4,
	}).FileInfo(), nil
}
func (s *copySource) Open(context.Context, string) (cloudfs.File, error) {
	return &copyReader{
		Reader: strings.NewReader("data"),
	}, nil
}
func (s *copySource) Remove(context.Context, string) error { s.removed = true; return nil }

type copyReader struct {
	*strings.Reader
}

func (r *copyReader) Close() error { return nil }
func TestCopyCloseFailure(t *testing.T) {
	source, target := &copySource{}, &failingWriterFS{}
	err := cloudfs.CopyBetween(context.Background(), source, "/file", target, "/file")
	if !errors.Is(err, errUpload) || !target.closed {
		t.Fatalf("err=%v closed=%t", err, target.closed)
	}
}

func TestConfigFormats(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("formats"), 0600); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(root)
	formats := map[string]string{
		"json": `{"MixedCase":{"driver":"local","option":{"path":` + string(quoted) + `}}}`,
		"yaml": "MixedCase:\n  driver: local\n  option:\n    path: " + string(quoted) + "\n",
		"yml":  "MixedCase:\n  driver: local\n  option:\n    path: " + string(quoted) + "\n",
		"toml": "[MixedCase]\ndriver = \"local\"\n[MixedCase.option]\npath = " + string(quoted) + "\n",
	}
	var identity string
	for ext, data := range formats {
		t.Run(ext, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "config."+ext)
			if err := os.WriteFile(filename, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := cliRun(filename, "cat", "MixedCase:/file")
			if err != nil || out != "formats" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			drivers, err := loadDrivers(filename)
			if err != nil {
				t.Fatal(err)
			}
			got := drivers.identity("mixedcase", "/")
			if identity == "" {
				identity = got
			} else if got != identity {
				t.Fatal("equivalent formats changed state identity")
			}
		})
	}
}

func TestInvalidConfigs(t *testing.T) {
	for _, test := range []struct {
		ext, data string
	}{
		{"json", `{`}, {"yaml", "name: ["}, {"toml", "[name"},
		{"json", `{}`}, {"yaml", "name:\n  driver: local\n"},
		{"json", `{"name":{"driver":"local","option":[]}}`},
		{"json", `{"bad:name":{"driver":"local","option":{}}}`},
		{"json", `{"drivers":{"name":{"driver":"local","option":{}}}}`},
		{"txt", `{}`},
	} {
		t.Run(test.ext+test.data, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "config."+test.ext)
			if err := os.WriteFile(filename, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadDrivers(filename); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}

func TestLocalAndNamedPaths(t *testing.T) {
	config, left, right := namedPair(t)
	t.Chdir(t.TempDir())
	if err := os.WriteFile("local.txt", []byte("mixed"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"cp", "local.txt", "right:/uploaded.txt"},
		{"cp", "right:/uploaded.txt", "./downloaded.txt"},
		{"mv", "./downloaded.txt", "left:/moved.txt"},
	} {
		if _, err := cliRun(config, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, file := range []string{filepath.Join(left, "moved.txt"), filepath.Join(right, "uploaded.txt")} {
		if data, err := os.ReadFile(file); err != nil || string(data) != "mixed" {
			t.Fatalf("%s: %q %v", file, data, err)
		}
	}
	if _, err := os.Stat("downloaded.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Mkdir("source", 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile("source/file", []byte("synced"), 0600)
	os.Mkdir(filepath.Join(right, "backup"), 0755)
	if _, err := cliRun(config, "sync", "source", "right:/backup"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(right, "backup/file")); err != nil || string(data) != "synced" {
		t.Fatalf("%q %v", data, err)
	}
	if out, err := cliRun(config, "cat", "local.txt"); err != nil || out != "mixed" {
		t.Fatalf("local path affected by config: %q %v", out, err)
	}
}

func TestDefaultConfigDiscovery(t *testing.T) {
	for _, extension := range []string{"json", "yaml", "yml", "toml"} {
		t.Run(extension, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(t.TempDir())
			if err := os.WriteFile(filepath.Join(root, "file"), []byte("default"), 0600); err != nil {
				t.Fatal(err)
			}
			quoted, _ := json.Marshal(root)
			data := `{"store":{"driver":"local","option":{"path":` + string(quoted) + `}}}`
			if extension == "yaml" || extension == "yml" {
				data = "store:\n  driver: local\n  option:\n    path: " + string(quoted) + "\n"
			}
			if extension == "toml" {
				data = "[store]\ndriver = \"local\"\n[store.option]\npath = " + string(quoted) + "\n"
			}
			if err := os.WriteFile("config."+extension, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := run(context.Background(), []string{"cloudfs", "cat", "store:/file"}, nil, &out, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != "default" {
				t.Fatal(out.String())
			}
		})
	}
}

func TestDefaultConfigPrecedenceAndFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := loadDrivers(""); err != nil {
		t.Fatalf("missing default config: %v", err)
	}
	good := `{"store":{"driver":"local","option":{"path":"/"}}}`
	if err := os.WriteFile("config.json", []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config.yaml", []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDrivers(""); err != nil {
		t.Fatalf("wrong default priority: %v", err)
	}
	if _, err := loadDrivers("config.yaml"); err == nil {
		t.Fatal("explicit config fell back to default")
	}
	if _, err := loadDrivers("missing.json"); err == nil {
		t.Fatal("explicit missing config fell back to default")
	}
	if err := os.WriteFile("config.json", []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("explicit.json", []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDrivers("explicit.json"); err != nil {
		t.Fatalf("default overrode explicit config: %v", err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"cloudfs", "mkdir", "should-not-exist"}, nil, &out, &out); err == nil {
		t.Fatal("invalid default config ignored")
	}
	if _, err := os.Stat("should-not-exist"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid config allowed writes")
	}
}
