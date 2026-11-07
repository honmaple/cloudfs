package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver"
	_ "github.com/honmaple/cloudfs/driver/all"
	"github.com/spf13/viper"
)

type driverConfig struct {
	Driver      string             `json:"driver"`
	Option      json.RawMessage    `json:"option"`
	Middlewares []middlewareConfig `json:"middlewares,omitempty"`
}

type driverSet struct {
	configs map[string]driverConfig
	opened  map[string]cloudfs.FS
}

func loadDrivers(filename string) (*driverSet, error) {
	set := &driverSet{
		configs: make(map[string]driverConfig),
		opened:  make(map[string]cloudfs.FS),
	}
	if filename == "" {
		for _, candidate := range []string{"config.json", "config.yaml", "config.yml", "config.toml"} {
			if _, err := os.Stat(candidate); err == nil {
				filename = candidate
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("cannot access configuration %s", candidate)
			}
		}
		if filename == "" {
			return set, nil
		}
	}
	extension := strings.ToLower(filepath.Ext(filename))
	switch extension {
	case ".json", ".yaml", ".yml", ".toml":
	default:
		return nil, errors.New("configuration must use .json, .yaml, .yml, or .toml")
	}
	config := viper.New()
	config.SetConfigFile(filename)
	config.SetConfigType(strings.TrimPrefix(extension, "."))
	if err := config.ReadInConfig(); err != nil {
		// Parser errors can include source lines containing credentials.
		return nil, errors.New("cannot read configuration; check file path, permissions, and syntax")
	}
	settings := config.AllSettings()
	if len(settings) == 0 {
		return nil, errors.New("configuration contains no drivers")
	}
	for name, value := range settings {
		if strings.ContainsAny(name, ":/\\") || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("invalid driver name %q", name)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, errors.New("invalid driver configuration")
		}
		var configured driverConfig
		if json.Unmarshal(data, &configured) != nil || configured.Driver == "" || len(configured.Option) == 0 || configured.Option[0] != '{' {
			return nil, fmt.Errorf("driver %q requires driver and option object", name)
		}
		for index, item := range configured.Middlewares {
			if _, err := item.wrapper(); err != nil {
				return nil, fmt.Errorf("driver %q middleware %d: %w", name, index+1, err)
			}
		}
		set.configs[name] = configured
	}
	return set, nil
}

func (s *driverSet) resolve(value string) (cloudfs.FS, string, string, error) {
	if value == "" {
		return nil, "", "", errors.New("path must not be empty")
	}
	name, p := "", value
	before, after, named := strings.Cut(value, ":")
	named = named && before != "" && !strings.ContainsAny(before, "/\\") &&
		(after == "" || strings.HasPrefix(after, "/")) && filepath.VolumeName(value) == ""
	if named {
		name, p = strings.ToLower(before), "/"+strings.TrimPrefix(after, "/")
	} else {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return nil, "", "", err
		}
		volume := filepath.VolumeName(absolute)
		name = "\x00local:" + volume
		option, _ := json.Marshal(map[string]string{"path": volume + string(filepath.Separator)})
		s.configs[name] = driverConfig{
			Driver: "local",
			Option: option,
		}
		p = filepath.ToSlash(strings.TrimPrefix(absolute, volume))
	}
	cfg, ok := s.configs[name]
	if !ok {
		return nil, "", "", fmt.Errorf("unknown driver %q", name)
	}
	fsys := s.opened[name]
	if fsys == nil {
		var err error
		fsys, err = driver.NewFromString(cfg.Driver, string(cfg.Option))
		if err != nil {
			return nil, "", "", fmt.Errorf("cannot initialize driver %q; %s", name, err.Error())
		}
		for index, item := range cfg.Middlewares {
			wrap, err := item.wrapper()
			if err == nil {
				var wrapped cloudfs.FS
				wrapped, err = wrap(fsys)
				if err == nil {
					fsys = wrapped
				}
			}
			if err != nil {
				// Close the successfully constructed stack; do not echo secrets.
				closeErr := fsys.Close()
				return nil, "", "", errors.Join(fmt.Errorf("driver %q middleware %d (%s) initialization failed", name, index+1, item.Type), closeErr)
			}
		}
		s.opened[name] = fsys
	}
	return fsys, p, name, nil
}

// identity binds state to configured endpoints without writing credentials.
func (s *driverSet) identity(name, p string) string {
	data, _ := json.Marshal(s.configs[name])
	sum := sha256.Sum256(data)
	return name + ":" + path.Clean(p) + ":" + hex.EncodeToString(sum[:])
}

// Reject overlapping paths, including two names for overlapping local roots.
func (s *driverSet) checkOverlap(left, src, right, dst string) error {
	a, b := s.configs[left], s.configs[right]
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	same := left == right || string(aJSON) == string(bJSON)
	if a.Driver == "local" && b.Driver == "local" {
		var x, y struct {
			Path string `json:"path"`
		}
		json.Unmarshal(a.Option, &x)
		json.Unmarshal(b.Option, &y)
		// Path mappings are applied from the outermost middleware inward.
		paths := []string{src, dst}
		for index, cfg := range []driverConfig{a, b} {
			for i := len(cfg.Middlewares) - 1; i >= 0; i-- {
				item := cfg.Middlewares[i]
				var opt struct {
					Prefix string `json:"prefix"`
				}
				json.Unmarshal(item.Option, &opt)
				switch item.Type {
				case "prefix":
					paths[index] = path.Join(opt.Prefix, paths[index])
				case "trim_prefix":
					paths[index] = strings.TrimPrefix(paths[index], opt.Prefix)
				}
			}
		}
		src, dst = paths[0], paths[1]
		src, dst = filepath.Join(x.Path, filepath.FromSlash(src)), filepath.Join(y.Path, filepath.FromSlash(dst))
		same = true
		// Resolve existing symlinks where possible before checking containment.
		if p, err := filepath.EvalSymlinks(src); err == nil {
			src = p
		}
		if p, err := filepath.EvalSymlinks(dst); err == nil {
			dst = p
		}
	}
	if same {
		src, dst = path.Clean(filepath.ToSlash(src)), path.Clean(filepath.ToSlash(dst))
		if src == dst || strings.HasPrefix(dst, strings.TrimSuffix(src, "/")+"/") || strings.HasPrefix(src, strings.TrimSuffix(dst, "/")+"/") {
			return errors.New("source and destination must not overlap")
		}
	}
	return nil
}

func (s *driverSet) close() error {
	var err error
	for name, fsys := range s.opened {
		if e := fsys.Close(); e != nil {
			err = errors.Join(err, fmt.Errorf("close driver %q: %w", name, e))
		}
	}
	return err
}

// driverPair contains resolved endpoints after transfer-path validation.
type driverPair struct {
	srcFS   cloudfs.FS
	dstFS   cloudfs.FS
	src     string
	dst     string
	srcName string
	dstName string
}

func (s *driverSet) pair(src, dst string) (*driverPair, error) {
	srcFS, src, srcName, err := s.resolve(src)
	if err != nil {
		return nil, err
	}
	dstFS, dst, dstName, err := s.resolve(dst)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(src+dst, "?") {
		return nil, errors.New("transfer paths must not contain query parameters")
	}
	if err = s.checkOverlap(srcName, src, dstName, dst); err != nil {
		return nil, err
	}
	return &driverPair{
		srcFS:   srcFS,
		dstFS:   dstFS,
		src:     src,
		dst:     dst,
		srcName: srcName,
		dstName: dstName,
	}, nil
}
