package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredMiddlewares(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "inner/outer"), 0755)
	quoted, _ := json.Marshal(root)
	formats := map[string]string{
		"json": `{"store":{"driver":"local","option":{"path":` + string(quoted) + `},"middlewares":[{"type":"prefix","option":{"prefix":"/inner"}},{"type":"prefix","option":{"prefix":"/outer"}},{"type":"encrypt","option":{"password":"test-secret","version":"v2"}},{"type":"compress"}]}}`,
		"yaml": "store:\n  driver: local\n  option:\n    path: " + string(quoted) + "\n  middlewares:\n    - type: prefix\n      option:\n        prefix: /inner\n    - type: prefix\n      option:\n        prefix: /outer\n    - type: encrypt\n      option:\n        password: test-secret\n        version: v2\n    - type: compress\n",
		"toml": "[store]\ndriver = \"local\"\n[store.option]\npath = " + string(quoted) + "\n[[store.middlewares]]\ntype = \"prefix\"\n[store.middlewares.option]\nprefix = \"/inner\"\n[[store.middlewares]]\ntype = \"prefix\"\n[store.middlewares.option]\nprefix = \"/outer\"\n[[store.middlewares]]\ntype = \"encrypt\"\n[store.middlewares.option]\npassword = \"test-secret\"\nversion = \"v2\"\n[[store.middlewares]]\ntype = \"compress\"\n",
	}
	input := filepath.Join(t.TempDir(), "input")
	os.WriteFile(input, []byte("middleware round trip"), 0600)
	for ext, data := range formats {
		t.Run(ext, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config."+ext)
			os.WriteFile(config, []byte(data), 0600)
			if _, err := cliRun(config, "cp", input, "store:/file"); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(root, "inner/outer/file"))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) == "middleware round trip" {
				t.Fatal("content middleware was not applied")
			}
			if out, err := cliRun(config, "cat", "store:/file"); err != nil || out != "middleware round trip" {
				t.Fatalf("%q %v", out, err)
			}
		})
	}
}

func TestConfiguredPaginationAndOverlap(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "data"), 0755)
	os.WriteFile(filepath.Join(root, "data/a"), []byte("a"), 0600)
	os.WriteFile(filepath.Join(root, "data/b"), []byte("b"), 0600)
	config := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(map[string]any{"store": map[string]any{"driver": "local", "option": map[string]string{"path": root}, "middlewares": []any{
		map[string]any{"type": "prefix", "option": map[string]string{"prefix": "/data"}},
		map[string]any{"type": "cache", "option": map[string]int{"expire_time": 60}},
		map[string]any{"type": "rate_limit", "option": map[string]int{"limit": 1, "burst": 100}},
		map[string]any{"type": "pagination"},
	}}})
	os.WriteFile(config, data, 0600)
	out, err := cliRun(config, "ls", "store:/?page=2&page_size=1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"b"`) || strings.Contains(out, `"a"`) {
		t.Fatalf("pagination=%s", out)
	}
	if _, err := cliRun(config, "cp", "store:/a", filepath.Join(root, "data/a")); err == nil {
		t.Fatal("middleware hid overlapping destination")
	}
}

func TestInvalidMiddlewareConfig(t *testing.T) {
	for _, item := range []string{
		`{"type":"unknown"}`, `{"type":"predicate"}`, `{"type":"encrypt","option":{"password":""}}`,
		`{"type":"prefix","option":{"prefix":"relative"}}`, `{"type":"cache","option":{"expire_time":"bad"}}`,
		`{"type":"compress","option":{"level":20}}`, `{"type":"cache","option":{"typo":1}}`,
	} {
		t.Run(item, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			// Validation must happen without connecting to the invalid underlying driver.
			os.WriteFile(config, []byte(`{"store":{"driver":"missing","option":{"secret":"do-not-print"},"middlewares":[`+item+`]}}`), 0600)
			_, err := loadDrivers(config)
			if err == nil || !strings.Contains(err.Error(), "middleware") {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "do-not-print") {
				t.Fatal("configuration leaked")
			}
		})
	}
}
