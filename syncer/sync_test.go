package syncer_test

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	stdpath "path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver/local"
	"github.com/honmaple/cloudfs/middleware"
	"github.com/honmaple/cloudfs/syncer"
)

func TestSync(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	putSyncFile(t, srcDir, "dir/file", "source")
	putSyncFile(t, dstDir, "dir/file", "target") // Same size; different modification times trigger a copy.
	stamp := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(srcDir, "dir/file"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	putSyncFile(t, dstDir, "extra/file", "keep")
	if err := os.Mkdir(filepath.Join(srcDir, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := syncer.Sync(ctx, src, dst, syncer.Option{OnConflict: syncer.PreferSrc()}); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "dir/file", "source")
	wantSyncFile(t, dstDir, "extra/file", "keep")
	if info, err := os.Stat(filepath.Join(dstDir, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("missing empty dir: %v", err)
	}
	if err := syncer.Sync(ctx, src, dst, syncer.Option{OnConflict: syncer.PreferSrc(), Delete: true}); err != nil {
		t.Fatal(err)
	}
	wantSyncAbsent(t, dstDir, "extra")
	// State comparison requires equal timestamps across sides for a no-op.
	info, err := os.Stat(filepath.Join(srcDir, "dir/file"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dstDir, "dir/file"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	// A no-op run must not need write capability.
	if err := syncer.Sync(ctx, src, &denySyncWrites{FS: dst}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncRejectsInvalidOptions(t *testing.T) {
	for _, test := range []struct {
		name    string
		options []syncer.Option
		want    string
	}{
		{"invalid mode", []syncer.Option{{Mode: syncer.Mode(255)}}, "invalid mode 255"},
		{"multiple options", []syncer.Option{{}, {}}, "expected at most one options value"},
		{"missing two-way state", []syncer.Option{{Mode: syncer.ModeTwoWay}}, "ModeTwoWay requires StateStore"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := syncer.Sync(context.Background(), nil, nil, test.options...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSyncPrefix(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "input/nested/file", "scoped")
	putSyncFile(t, srcDir, "outside-source", "excluded")
	putSyncFile(t, dstDir, "output/extra", "remove")
	putSyncFile(t, dstDir, "outside-target", "keep")
	src, err := cloudfs.New(src, middleware.PrefixFS("/input"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err = cloudfs.New(dst, middleware.PrefixFS("/output"))
	if err != nil {
		t.Fatal(err)
	}
	option := syncer.Option{Delete: true, StateStore: &singleSyncStore{}}
	for range 2 {
		if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
			t.Fatal(err)
		}
	}
	wantSyncFile(t, dstDir, "output/nested/file", "scoped")
	wantSyncAbsent(t, dstDir, "output/extra")
	wantSyncAbsent(t, dstDir, "output/outside-source")
	wantSyncFile(t, dstDir, "outside-target", "keep")
}

func syncPair(t *testing.T) (cloudfs.FS, cloudfs.FS, string, string) {
	t.Helper()
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src, err := local.New(&local.Option{Path: srcDir})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := local.New(&local.Option{Path: dstDir})
	if err != nil {
		t.Fatal(err)
	}
	return src, dst, srcDir, dstDir
}

func putSyncFile(t *testing.T, root, path, content string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func wantSyncFile(t *testing.T, root, path, content string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || string(data) != content {
		t.Fatalf("%s: got %q, %v; want %q", path, data, err, content)
	}
}

func removeSyncPath(t *testing.T, root, path string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
}

func wantSyncAbsent(t *testing.T, root, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, path)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s still exists: %v", path, err)
	}
}

type denySyncWrites struct{ cloudfs.FS }

func (*denySyncWrites) Create(context.Context, string) (cloudfs.FileWriter, error) {
	return nil, errors.New("unexpected write")
}

var errSyncTest = errors.New("injected sync failure")

func TestSyncComparesBeforeWriting(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "a-first", "must not copy")
	putSyncFile(t, srcDir, "z-conflict/child", "file")
	putSyncFile(t, dstDir, "z-conflict", "existing file")
	if err := syncer.Sync(context.Background(), src, dst); !errors.Is(err, syncer.ErrSyncConflict) {
		t.Fatalf("got %v", err)
	}
	wantSyncAbsent(t, dstDir, "a-first")
	wantSyncFile(t, dstDir, "z-conflict", "existing file")
}

// Track data access separately from metadata access to model a remote backend.
type observedSyncFS struct {
	cloudfs.FS
	dataOpens  int
	rejectData bool
	timeOffset time.Duration
	lists      map[string]int
	stats      map[string]int
}

func (f *observedSyncFS) Open(ctx context.Context, p string) (cloudfs.File, error) {
	if stdpath.Base(p) != ".cloudfs-bisync.json" {
		f.dataOpens++
		if f.rejectData {
			return nil, errors.New("unexpected remote data download")
		}
	}
	return f.FS.Open(ctx, p)
}

func (f *observedSyncFS) List(ctx context.Context, p string) ([]cloudfs.FileInfo, error) {
	if f.lists == nil {
		f.lists = make(map[string]int)
	}
	f.lists[p]++
	entries, err := f.FS.List(ctx, p)
	if err != nil {
		return nil, err
	}
	for i, entry := range entries {
		entries[i] = cloudfs.NewFileInfo(entry, func(out *cloudfs.Entry) { out.Path = entry.Path(); out.ModTime = entry.ModTime().Add(f.timeOffset) })
	}
	return entries, nil
}

func (f *observedSyncFS) Stat(ctx context.Context, p string) (cloudfs.FileInfo, error) {
	if f.stats == nil {
		f.stats = make(map[string]int)
	}
	f.stats[p]++
	info, err := f.FS.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	return cloudfs.NewFileInfo(info, func(out *cloudfs.Entry) { out.Path = info.Path(); out.ModTime = info.ModTime().Add(f.timeOffset) }), nil
}

func TestSyncScansOnceWithoutDownloadingForComparison(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "nested/file", "source")
	source := &observedSyncFS{FS: src}
	target := &observedSyncFS{FS: dst, rejectData: true}
	if err := syncer.Sync(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 1 || target.dataOpens != 0 {
		t.Fatalf("source opens=%d, target opens=%d", source.dataOpens, target.dataOpens)
	}
	if !reflect.DeepEqual(source.lists, map[string]int{"/": 1, "/nested": 1}) || !reflect.DeepEqual(target.lists, map[string]int{"/": 1}) {
		t.Fatalf("repeated scan: source=%v target=%v", source.lists, target.lists)
	}
	if !reflect.DeepEqual(source.stats, map[string]int{"/": 1, "/nested/file": 2}) || !reflect.DeepEqual(target.stats, map[string]int{"/": 1, "/nested/file": 1}) {
		t.Fatalf("unexpected stats: source=%v target=%v", source.stats, target.stats)
	}
	wantSyncFile(t, dstDir, "nested/file", "source")
}

func TestSyncChecksumIsExplicit(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "aaa")
	putSyncFile(t, dstDir, "file", "bbb")
	timestamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, root := range []string{srcDir, dstDir} {
		if err := os.Chtimes(filepath.Join(root, "file"), timestamp, timestamp); err != nil {
			t.Fatal(err)
		}
	}
	source, target := &observedSyncFS{FS: src}, &observedSyncFS{FS: dst}
	if err := syncer.Sync(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 0 || target.dataOpens != 0 {
		t.Fatal("metadata comparison inspected content")
	}
	wantSyncFile(t, dstDir, "file", "bbb")
	if err := syncer.Sync(context.Background(), source, target, syncer.Option{OnConflict: syncer.PreferSrc(), Checksum: true}); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 2 || target.dataOpens != 1 {
		t.Fatalf("checksum reread entire trees: source=%d target=%d", source.dataOpens, target.dataOpens)
	}
	wantSyncFile(t, dstDir, "file", "aaa")
}

type virtualWalkFS struct{ cloudfs.BaseFS }

func (virtualWalkFS) Stat(context.Context, string) (cloudfs.FileInfo, error) {
	return (&cloudfs.Entry{Name: "physical-root", Path: "/backend", IsDir: true}).FileInfo(), nil
}

func (virtualWalkFS) List(_ context.Context, p string) ([]cloudfs.FileInfo, error) {
	switch p {
	case "/":
		return []cloudfs.FileInfo{(&cloudfs.Entry{Name: "dir", Path: "/backend", IsDir: true}).FileInfo()}, nil
	case "/dir":
		return []cloudfs.FileInfo{(&cloudfs.Entry{Name: "file", Path: "/backend/dir", Size: 123}).FileInfo()}, nil
	default:
		return nil, fmt.Errorf("unexpected traversal path %s", p)
	}
}

func TestWalkDirUsesVirtualPaths(t *testing.T) {
	// Stat/List metadata can describe a backend path instead of the virtual root.
	// WalkDir must recurse using the path supplied to its callback.
	var names []string
	if err := cloudfs.WalkDir(context.Background(), virtualWalkFS{}, "/", func(p string, _ cloudfs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		names = append(names, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"/", "/dir", "/dir/file"}) {
		t.Fatalf("paths = %v", names)
	}
}

type singleSyncStore struct {
	data                             []byte
	saves                            int
	failLoad, failSave, saveThenFail bool
}

func (store *singleSyncStore) Load(context.Context) (*syncer.State, error) {
	if store.failLoad {
		return nil, errSyncTest
	}
	if len(store.data) == 0 {
		return nil, nil
	}
	var state syncer.State
	if err := json.Unmarshal(store.data, &state); err != nil {
		return nil, fmt.Errorf("%w: %w", syncer.ErrSyncState, err)
	}
	return &state, nil
}

func (store *singleSyncStore) Save(_ context.Context, state *syncer.State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	store.saves++
	if !store.failSave || store.saveThenFail {
		store.data = data
	}
	if store.failSave {
		store.failSave = false
		return errSyncTest
	}
	return nil
}

type sourceReadFault struct {
	cloudfs.FS
	remainingFailures int
	skipOpens         int
	failPath          string
}

func (f *sourceReadFault) Open(ctx context.Context, p string) (cloudfs.File, error) {
	file, err := f.FS.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	if f.skipOpens > 0 {
		f.skipOpens--
		return file, nil
	}
	if f.remainingFailures > 0 && (f.failPath == "" || f.failPath == p) {
		f.remainingFailures--
		return &readFaultFile{File: file, remaining: 2}, nil
	}
	return file, nil
}

type readFaultFile struct {
	cloudfs.File
	remaining int
}

func (f *readFaultFile) Read(p []byte) (int, error) {
	if f.remaining == 0 {
		return 0, errSyncTest
	}
	if len(p) > f.remaining {
		p = p[:f.remaining]
	}
	n, err := f.File.Read(p)
	f.remaining -= n
	return n, err
}

type targetStatFault struct {
	cloudfs.FS
	failures int
}

func (f *targetStatFault) Stat(ctx context.Context, p string) (cloudfs.FileInfo, error) {
	if p == "/file" && f.failures > 0 {
		f.failures--
		return nil, errSyncTest
	}
	return f.FS.Stat(ctx, p)
}

func TestSyncRecordedNoopWithDifferentTimestamps(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	source := &observedSyncFS{FS: src}
	target := &observedSyncFS{FS: dst, timeOffset: 24 * time.Hour}
	store := &singleSyncStore{}
	option := syncer.Option{StateStore: store}
	if err := syncer.Sync(context.Background(), source, target, option); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte("contents")))
	if state.Target["file"].Hash != expectedHash {
		t.Fatalf("copy omitted hash with Checksum disabled: %s", state.Target["file"].Hash)
	}
	if store.saves != 1 {
		t.Fatalf("saves=%d", store.saves)
	}
	if err := syncer.Sync(context.Background(), source, target, option); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 1 || target.dataOpens != 0 || store.saves != 1 || source.lists["/"] != 2 {
		t.Fatalf("opens=%d/%d saves=%d lists=%v", source.dataOpens, target.dataOpens, store.saves, source.lists)
	}
	wantSyncFile(t, dstDir, "file", "contents")
}

func TestSyncFreshComparisonKeepsConfirmedFiles(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "a-finished", "first")
	putSyncFile(t, srcDir, "z-failed", "second file")
	source := &observedSyncFS{FS: src}
	fault := &pathReadFault{FS: source, path: "/z-failed"}
	store := &singleSyncStore{}
	option := syncer.Option{OnConflict: syncer.PreferSrc(), StateStore: store}
	if err := syncer.Sync(context.Background(), fault, dst, option); !errors.Is(err, errSyncTest) {
		t.Fatalf("got %v", err)
	}
	wantSyncFile(t, dstDir, "a-finished", "first")
	wantSyncFile(t, dstDir, "z-failed", "se")
	if len(store.data) == 0 {
		t.Fatal("confirmed file metadata was not saved after a later failure")
	}
	if err := syncer.Sync(context.Background(), fault, dst, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "z-failed", "second file")
	if source.dataOpens != 3 {
		t.Fatalf("completed file was recopied: %d opens", source.dataOpens)
	}
}

type pathReadFault struct {
	cloudfs.FS
	path   string
	failed bool
}

func (f *pathReadFault) Open(ctx context.Context, p string) (cloudfs.File, error) {
	file, err := f.FS.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	if p == f.path && !f.failed {
		f.failed = true
		return &readFaultFile{File: file, remaining: 2}, nil
	}
	return file, nil
}

func TestSyncFreshComparisonAfterMetadataFailure(t *testing.T) {
	for _, scenario := range []string{"stat", "save", "save-lost-reply"} {
		t.Run(scenario, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "contents")
			source := &observedSyncFS{FS: src}
			target := &observedSyncFS{FS: dst, timeOffset: 24 * time.Hour}
			store := &singleSyncStore{}
			var output cloudfs.FS = target
			switch scenario {
			case "stat":
				output = &targetStatFault{FS: target, failures: 1}
			case "save":
				store.failSave = true
			case "save-lost-reply":
				store.failSave, store.saveThenFail = true, true
			}
			option := syncer.Option{OnConflict: syncer.PreferSrc(), StateStore: store}
			if err := syncer.Sync(context.Background(), source, output, option); !errors.Is(err, errSyncTest) {
				t.Fatalf("got %v", err)
			}
			if err := syncer.Sync(context.Background(), source, output, option); err != nil {
				t.Fatal(err)
			}
			expectedOpens := 2
			if scenario == "save-lost-reply" {
				expectedOpens = 1
			}
			if source.dataOpens != expectedOpens || source.lists["/"] != 2 || target.lists["/"] != 2 {
				t.Fatalf("opens=%d scans=%v/%v", source.dataOpens, source.lists, target.lists)
			}
			wantSyncFile(t, dstDir, "file", "contents")
		})
	}
}

func TestSyncOnErrorControlsFileRetries(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	for _, path := range []string{"a-finished", "z-failed", "zz-after"} {
		putSyncFile(t, srcDir, path, "contents")
	}
	source := &observedSyncFS{FS: src}
	input := &sourceReadFault{FS: source, remainingFailures: 2, failPath: "/z-failed"}
	callbacks, retries := 0, 0
	option := syncer.Option{OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		path := failure.FileInfo.Path()
		callbacks++
		if path != "z-failed" || !errors.Is(failure.Err, errSyncTest) {
			t.Fatalf("unexpected file error: %s %v", path, failure.Err)
		}
		if retries < 3 {
			retries++
			return syncer.ErrorRetry, nil
		}
		return syncer.ErrorAbort, failure.Err
	}}
	if err := syncer.Sync(context.Background(), input, dst, option); err != nil {
		t.Fatal(err)
	}
	if callbacks != 2 || retries != 2 || source.dataOpens != 5 || source.lists["/"] != 1 {
		t.Fatalf("callbacks=%d retries=%d opens=%d lists=%v", callbacks, retries, source.dataOpens, source.lists)
	}
	for _, path := range []string{"a-finished", "z-failed", "zz-after"} {
		wantSyncFile(t, dstDir, path, "contents")
	}
}

func TestSyncMetadataAndStateFailuresBypassOnError(t *testing.T) {
	for _, failure := range []string{"stat", "load", "save"} {
		t.Run(failure, func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			putSyncFile(t, srcDir, "file", "contents")
			store := &singleSyncStore{}
			var target cloudfs.FS = dst
			switch failure {
			case "load":
				store.failLoad = true
			case "stat":
				target = &targetStatFault{FS: dst, failures: 1}
			case "save":
				store.failSave = true
			}
			called := false
			err := syncer.Sync(context.Background(), src, target, syncer.Option{StateStore: store, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
				called = true
				return syncer.ErrorRetry, nil
			}})
			if !errors.Is(err, errSyncTest) || called || len(store.data) != 0 {
				t.Fatalf("err=%v callback=%v state=%s", err, called, store.data)
			}
			if err.Error() != errSyncTest.Error() {
				t.Fatalf("unexpected error prefix: %v", err)
			}
		})
	}
}

func TestSyncNextCallComparesChangedSourceAfresh(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "original")
	source := &sourceReadFault{FS: src, remainingFailures: 1}
	option := syncer.Option{OnConflict: syncer.PreferSrc(), StateStore: &singleSyncStore{}}
	if err := syncer.Sync(context.Background(), source, dst, option); !errors.Is(err, errSyncTest) {
		t.Fatalf("got %v", err)
	}
	putSyncFile(t, srcDir, "file", "new source content")
	if err := syncer.Sync(context.Background(), source, dst, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "file", "new source content")
}

func TestSyncCancellationOverridesRetry(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	source := &observedSyncFS{FS: src}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := syncer.Sync(ctx, &sourceReadFault{FS: source, remainingFailures: 1}, dst, syncer.Option{OnError: func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
		cancel()
		return syncer.ErrorRetry, nil
	}})
	if !errors.Is(err, context.Canceled) || source.dataOpens != 1 {
		t.Fatalf("err=%v opens=%d", err, source.dataOpens)
	}
}

func TestSyncRejectsInvalidRecordingState(t *testing.T) {
	for _, data := range []string{"broken",
		`{"hash_algorithm":5,"version":2,"revision":"initial","source":{},"target":{}}`,
		`{"hash_algorithm":5,"version":1,"source":{},"target":{}}`,
		`{"hash_algorithm":5,"version":1,"revision":"initial","source":{"file":null},"target":{}}`,
		`{"hash_algorithm":5,"version":1,"revision":"initial","source":{"dir/file":{}},"target":{}}`,
		`{"hash_algorithm":5,"version":1,"revision":"initial","source":{"../escape":{}},"target":{}}`,
	} {
		src, dst, srcDir, dstDir := syncPair(t)
		putSyncFile(t, srcDir, "file", "contents")
		called := false
		err := syncer.Sync(context.Background(), src, dst, syncer.Option{StateStore: &singleSyncStore{data: []byte(data)}, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
			called = true
			return syncer.ErrorAbort, failure.Err
		}})
		if !errors.Is(err, syncer.ErrSyncState) || called {
			t.Fatalf("invalid state retried: %v", err)
		}
		wantSyncAbsent(t, dstDir, "file")
	}
}

type directoryListFault struct {
	cloudfs.FS
	failed bool
}

func (f *directoryListFault) List(ctx context.Context, p string) ([]cloudfs.FileInfo, error) {
	if p == "/nested" && !f.failed {
		f.failed = true
		return nil, errSyncTest
	}
	return f.FS.List(ctx, p)
}

func TestSyncScanErrorBypassesOnError(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "nested/file", "contents")
	putSyncFile(t, dstDir, "keep", "data")
	source := &observedSyncFS{FS: &directoryListFault{FS: src}}
	called := false
	err := syncer.Sync(context.Background(), source, dst, syncer.Option{Delete: true, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		called = true
		return syncer.ErrorRetry, nil
	}})
	if !errors.Is(err, errSyncTest) || called || source.lists["/nested"] != 1 {
		t.Fatalf("err=%v callback=%v lists=%v", err, called, source.lists)
	}
	wantSyncAbsent(t, dstDir, "nested")
	wantSyncFile(t, dstDir, "keep", "data")
}

func TestSyncRetryUsesScannedEntry(t *testing.T) {
	for _, checksum := range []bool{false, true} {
		t.Run(fmt.Sprint(checksum), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "contents")
			source := &observedSyncFS{FS: src}
			fault := &sourceReadFault{FS: source, remainingFailures: 1}
			expectedOpens := 2
			expectedStats := 3 // Two attempts, then a successful post-copy check.

			calls := 0
			err := syncer.Sync(context.Background(), fault, dst, syncer.Option{Checksum: checksum, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
				path := failure.FileInfo.Path()
				calls++
				if path != "file" || !errors.Is(failure.Err, errSyncTest) {
					t.Fatalf("unexpected error: %s %v", path, failure.Err)
				}
				return syncer.ErrorRetry, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || source.dataOpens != expectedOpens || !reflect.DeepEqual(source.stats, map[string]int{"/": 1, "/file": expectedStats}) || source.lists["/"] != 1 {
				t.Fatalf("calls=%d opens=%d stats=%v lists=%v", calls, source.dataOpens, source.stats, source.lists)
			}
			wantSyncFile(t, dstDir, "file", "contents")
		})
	}
}

func TestSyncFreshComparisonAfterDeletionError(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	putSyncFile(t, dstDir, "extra/child", "remove")
	target := &fileOperationFaultFS{FS: dst}
	option := syncer.Option{Delete: true, StateStore: &singleSyncStore{}}
	if err := syncer.Sync(context.Background(), src, target, option); !errors.Is(err, errSyncTest) {
		t.Fatalf("got %v", err)
	}
	if err := syncer.Sync(context.Background(), src, target, option); err != nil {
		t.Fatal(err)
	}
	wantSyncAbsent(t, dstDir, "extra")
	wantSyncFile(t, dstDir, "file", "contents")
}

func TestSyncDropsMetadataForFilesRemovedOnBothSides(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	store := &singleSyncStore{}
	option := syncer.Option{StateStore: store}
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	removeSyncPath(t, srcDir, "file")
	removeSyncPath(t, dstDir, "file")
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	if store.saves != 2 {
		t.Fatalf("saves=%d", store.saves)
	}
	// An equal new pair is confirmed from the new scans, with no old operation IDs.
	putSyncFile(t, srcDir, "file", "new contents")
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "file", "new contents")
}

func TestSyncRetryFailureReentersOnError(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	source := &sourceReadFault{FS: src, remainingFailures: 2}
	calls := 0
	err := syncer.Sync(context.Background(), source, dst, syncer.Option{OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		calls++
		if calls == 1 {
			return syncer.ErrorRetry, nil
		}
		return syncer.ErrorAbort, failure.Err
	}})
	if !errors.Is(err, errSyncTest) || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestSyncChecksumErrorBypassesOnError(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	putSyncFile(t, dstDir, "file", "previous")
	source := &observedSyncFS{FS: &sourceReadFault{FS: src, remainingFailures: 1}}
	called := false
	err := syncer.Sync(context.Background(), source, dst, syncer.Option{Checksum: true, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		called = true
		return syncer.ErrorRetry, nil
	}})
	if !errors.Is(err, errSyncTest) || called || source.dataOpens != 1 || source.lists["/"] != 1 {
		t.Fatalf("err=%v callback=%v opens=%d lists=%v", err, called, source.dataOpens, source.lists)
	}
	wantSyncFile(t, dstDir, "file", "previous")
}

// Fail each data mutation once so OnError must explicitly select ErrorRetry.
type fileOperationFaultFS struct {
	cloudfs.FS
	mkdirFailed, removeFailed bool
}

func (f *fileOperationFaultFS) MakeDir(ctx context.Context, path string) error {
	if !f.mkdirFailed {
		f.mkdirFailed = true
		return errSyncTest
	}
	return f.FS.MakeDir(ctx, path)
}

func (f *fileOperationFaultFS) Remove(ctx context.Context, path string) error {
	if !f.removeFailed {
		f.removeFailed = true
		return errSyncTest
	}
	return f.FS.Remove(ctx, path)
}

func TestSyncOnErrorRetriesCurrentDirectoryOrRemoval(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "nested/file", "contents")
	putSyncFile(t, dstDir, "extra", "remove")
	calls := make(map[string]int)
	target := &observedSyncFS{FS: dst}
	err := syncer.Sync(context.Background(), src, &fileOperationFaultFS{FS: target}, syncer.Option{Delete: true, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		path := failure.FileInfo.Path()
		calls[path]++
		if !errors.Is(failure.Err, errSyncTest) {
			t.Fatalf("unexpected error: %v", failure.Err)
		}
		return syncer.ErrorRetry, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, map[string]int{"nested": 1, "extra": 1}) {
		t.Fatalf("callbacks=%v", calls)
	}
	if !reflect.DeepEqual(target.stats, map[string]int{"/": 1, "/nested/file": 1}) {
		t.Fatalf("mkdir retry queried metadata: %v", target.stats)
	}
	if !reflect.DeepEqual(target.lists, map[string]int{"/": 1}) {
		t.Fatalf("remove error triggered an extra query: %v", target.lists)
	}
	wantSyncFile(t, dstDir, "nested/file", "contents")
	wantSyncAbsent(t, dstDir, "extra")
}

func TestSyncOnErrorIgnorePreservesFileBaseline(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "a-failed", "original")
	putSyncFile(t, srcDir, "z-after", "before")
	store := &singleSyncStore{}
	option := syncer.Option{OnConflict: syncer.PreferSrc(), StateStore: store}
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	putSyncFile(t, srcDir, "a-failed", "new contents")
	putSyncFile(t, srcDir, "z-after", "updated after")
	source := &observedSyncFS{FS: src}
	calls := 0
	option.OnError = func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		path := failure.FileInfo.Path()
		calls++
		if path != "a-failed" || !errors.Is(failure.Err, errSyncTest) {
			t.Fatalf("unexpected error: %s %v", path, failure.Err)
		}
		return syncer.ErrorSkip, nil
	}
	if err := syncer.Sync(context.Background(), &sourceReadFault{FS: source, remainingFailures: 1, failPath: "/a-failed"}, dst, option); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || source.dataOpens != 2 {
		t.Fatalf("calls=%d opens=%d", calls, source.dataOpens)
	}
	wantSyncFile(t, dstDir, "z-after", "updated after")
	wantSyncFile(t, dstDir, "a-failed", "ne")
	// The ignored file must still need copying; the completed sibling must not.
	option.OnError = nil
	if err := syncer.Sync(context.Background(), source, dst, option); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 3 {
		t.Fatalf("ignored baseline advanced or completed file recopied: opens=%d", source.dataOpens)
	}
	wantSyncFile(t, dstDir, "a-failed", "new contents")
}

func TestSyncOnErrorIgnoreDirectoryAndRemoval(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "nested/file", "contents")
	putSyncFile(t, srcDir, "z-after", "continued")
	putSyncFile(t, dstDir, "extra", "keep until retry")
	target := &fileOperationFaultFS{FS: dst}
	calls := make(map[string]int)
	option := syncer.Option{Delete: true, StateStore: &singleSyncStore{}, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		path := failure.FileInfo.Path()
		calls[path]++
		return syncer.ErrorSkip, nil
	}}
	if err := syncer.Sync(context.Background(), src, target, option); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, map[string]int{"nested": 1, "extra": 1}) {
		t.Fatalf("callbacks=%v", calls)
	}
	wantSyncAbsent(t, dstDir, "nested")
	wantSyncFile(t, dstDir, "extra", "keep until retry")
	wantSyncFile(t, dstDir, "z-after", "continued")
	option.OnError = nil
	if err := syncer.Sync(context.Background(), src, target, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "nested/file", "contents")
	wantSyncAbsent(t, dstDir, "extra")
}

func TestSyncUsesPublicMetadataRecords(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	putSyncFile(t, dstDir, "file", "contents")
	stamp := time.Unix(1000000000, 0)
	if err := os.Chtimes(filepath.Join(dstDir, "file"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	srcInfo, err := src.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := dst.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	state := &syncer.State{
		Version: 1, Revision: "initial", HashAlgorithm: crypto.SHA256,
		Source: map[string]*syncer.Record{"file": {Size: srcInfo.Size(), ModTime: srcInfo.ModTime().UTC()}},
		Target: map[string]*syncer.Record{"file": {Size: dstInfo.Size(), ModTime: dstInfo.ModTime().UTC()}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	store := &singleSyncStore{data: data}
	source := &observedSyncFS{FS: src, rejectData: true}
	target := &observedSyncFS{FS: dst, rejectData: true}
	if err := syncer.Sync(ctx, source, target, syncer.Option{StateStore: store}); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 0 || target.dataOpens != 0 || store.saves != 0 {
		t.Fatalf("unnecessary work: opens=%d/%d saves=%d", source.dataOpens, target.dataOpens, store.saves)
	}
}

type operationContextFS struct {
	cloudfs.FS
	openCtx, createCtx, mkdirCtx, removeCtx context.Context
}

func (f *operationContextFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	f.openCtx = ctx
	return f.FS.Open(ctx, path)
}

func (f *operationContextFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	f.createCtx = ctx
	return f.FS.Create(ctx, path)
}

func (f *operationContextFS) MakeDir(ctx context.Context, path string) error {
	f.mkdirCtx = ctx
	return f.FS.MakeDir(ctx, path)
}

func (f *operationContextFS) Remove(ctx context.Context, path string) error {
	f.removeCtx = ctx
	return f.FS.Remove(ctx, path)
}

func TestSyncOnErrorUsesSynchronizationContext(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "nested/file", "contents")
	putSyncFile(t, dstDir, "extra", "remove")
	source, target := &operationContextFS{FS: src}, &operationContextFS{FS: dst}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "sync")
	calls := 0
	err := syncer.Sync(ctx, &sourceReadFault{FS: source, remainingFailures: 1}, &fileOperationFaultFS{FS: target}, syncer.Option{
		Delete: true,
		OnError: func(handlerCtx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
			calls++
			if handlerCtx != ctx {
				t.Fatal("handler did not receive synchronization context")
			}
			return syncer.ErrorRetry, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("callbacks=%d", calls)
	}
	for operation, operationCtx := range map[string]context.Context{"open": source.openCtx, "create": target.createCtx, "mkdir": target.mkdirCtx, "remove": target.removeCtx} {
		if operationCtx != ctx {
			t.Fatalf("%s did not receive synchronization context", operation)
		}
	}
	wantSyncFile(t, dstDir, "nested/file", "contents")
	wantSyncAbsent(t, dstDir, "extra")
}

type beforeCopyFS struct {
	cloudfs.FS
	opens      int
	beforeCopy func()
}

func (f *beforeCopyFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	f.opens++
	if f.opens == 2 {
		f.beforeCopy()
	}
	return f.FS.Open(ctx, path)
}

func TestSyncChecksumRejectsChangedTransfer(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "aaa")
	putSyncFile(t, dstDir, "file", "bbb")
	// The second source Open is the copy, after the scan compared hashes.
	// Keep metadata unchanged so only the transfer hash reveals the change.
	info, err := os.Stat(filepath.Join(srcDir, "file"))
	if err != nil {
		t.Fatal(err)
	}
	source := &beforeCopyFS{FS: src, beforeCopy: func() {
		putSyncFile(t, srcDir, "file", "ccc")
		if err := os.Chtimes(filepath.Join(srcDir, "file"), info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}}
	target := &observedSyncFS{FS: dst}
	store := &singleSyncStore{}
	if err := syncer.Sync(context.Background(), source, target, syncer.Option{OnConflict: syncer.PreferSrc(), Checksum: true, StateStore: store}); !errors.Is(err, syncer.ErrSourceChanged) {
		t.Fatalf("got %v", err)
	}
	if source.opens != 2 || target.dataOpens != 1 {
		t.Fatalf("unexpected checksum reads: source=%d target=%d", source.opens, target.dataOpens)
	}
	if store.saves != 0 {
		t.Fatal("changed transfer advanced state")
	}
	wantSyncFile(t, dstDir, "file", "ccc")
}

func TestSyncCanChangeChecksumOption(t *testing.T) {
	for _, mode := range []syncer.Mode{syncer.ModeOneWay, syncer.ModeTwoWay} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "contents")
			source := &observedSyncFS{FS: src}
			target := &observedSyncFS{FS: dst, timeOffset: 24 * time.Hour}
			option := syncer.Option{Mode: mode, StateStore: &singleSyncStore{}}
			if err := syncer.Sync(context.Background(), source, target, option); err != nil {
				t.Fatal(err)
			}
			expectedOpens := source.dataOpens
			for _, checksum := range []bool{true, false} {
				option.Checksum = checksum
				if err := syncer.Sync(context.Background(), source, target, option); err != nil {
					t.Fatal(err)
				}
				if source.dataOpens != expectedOpens {
					t.Fatalf("changing Checksum reopened an unchanged file: opens=%d want=%d", source.dataOpens, expectedOpens)
				}
			}
			wantSyncFile(t, dstDir, "file", "contents")
		})
	}
}

func TestSyncStateStoreEnablesRecording(t *testing.T) {
	for _, provided := range []bool{false, true} {
		t.Run(fmt.Sprint(provided), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "contents")
			store := &singleSyncStore{failLoad: true, failSave: true}
			var stateStore syncer.StateStore
			if provided {
				stateStore = store
			}
			err := syncer.Sync(context.Background(), src, dst, syncer.Option{
				StateStore: stateStore,
			})
			if provided {
				if !errors.Is(err, errSyncTest) {
					t.Fatalf("provided store was not loaded: %v", err)
				}
				wantSyncAbsent(t, dstDir, "file")
			} else {
				if err != nil {
					t.Fatal(err)
				}
				wantSyncFile(t, dstDir, "file", "contents")
			}
			if store.saves != 0 {
				t.Fatalf("unexpected saves: %d", store.saves)
			}
		})
	}
}

func TestSyncCallbacksUseScannedFileInfo(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "dir/file", "copy contents")
	putSyncFile(t, dstDir, "extra", "remove contents")
	source := &observedSyncFS{FS: src}
	target := &observedSyncFS{FS: dst}
	fault := &sourceReadFault{FS: source, remainingFailures: 1}
	started := make(map[string]syncer.FileInfo)
	errorsSeen := make(map[string]syncer.FileInfo)
	err := syncer.Sync(context.Background(), fault, &fileOperationFaultFS{FS: target}, syncer.Option{
		Delete: true, Checksum: true,
		OnEvent: taskEvents(func(notification syncer.Event) {
			if _, ok := notification.(syncer.ProgressEvent); ok {
				return
			}

			event := notification.(syncer.TaskEvent)
			info := event.FileInfo
			if info == nil {
				t.Fatal("missing task FileInfo")
			}
			if event.Status == syncer.StatusStarted {
				started[info.Path()] = info
			} else if !reflect.DeepEqual(info, started[info.Path()]) {
				t.Fatalf("task snapshot changed: %s", info.Path())
			}
		}),
		OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
			if failure.FileInfo == nil || !reflect.DeepEqual(failure.FileInfo, started[failure.FileInfo.Path()]) {
				t.Fatal("error and task FileInfo differ")
			}
			errorsSeen[failure.FileInfo.Path()] = failure.FileInfo
			return syncer.ErrorRetry, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 3 || len(errorsSeen) != 3 {
		t.Fatalf("tasks=%v errors=%v", started, errorsSeen)
	}
	for path, contents := range map[string]string{"dir/file": "copy contents", "extra": "remove contents"} {
		info := errorsSeen[path]

		if info == nil || info.IsDir() || info.Size() != int64(len(contents)) || info.ModTime().IsZero() || info.Hash() != "" {
			t.Fatalf("invalid scanned file information for %s: %+v", path, info)
		}
	}
	if info := errorsSeen["dir"]; !info.IsDir() || info.Size() != 0 || info.Hash() != "" {
		t.Fatal("invalid scanned directory information")
	}
	if source.dataOpens != 2 || target.dataOpens != 0 || !reflect.DeepEqual(source.lists, map[string]int{"/": 1, "/dir": 1}) {
		t.Fatalf("extra callback I/O: opens=%d/%d lists=%v", source.dataOpens, target.dataOpens, source.lists)
	}
	wantSyncFile(t, dstDir, "dir/file", "copy contents")
	wantSyncAbsent(t, dstDir, "extra")
}

func TestSyncMetadataRevisionLifecycle(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			ctx := context.Background()
			store := &singleSyncStore{}
			run := func() {
				t.Helper()
				var err error
				if mode == "sync" {
					err = syncer.Sync(ctx, src, dst, syncer.Option{OnConflict: syncer.PreferSrc(), StateStore: store})
				} else {
					err = syncer.Sync(ctx, src, dst, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						StateStore: store})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			putSyncFile(t, srcDir, "file", "original")
			startedAt := time.Now()
			run()
			before, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if before.UpdatedAt.Before(startedAt) || before.UpdatedAt.After(time.Now()) || before.UpdatedAt.Location() != time.UTC {
				t.Fatalf("invalid update time: %v", before.UpdatedAt)
			}
			hash, err := hex.DecodeString(before.Revision)
			if err != nil || len(hash) != sha256.Size || before.Version != 1 {
				t.Fatalf("invalid content revision: %q", before.Revision)
			}
			run()
			if store.saves != 1 {
				t.Fatal("unchanged metadata was saved again")
			}
			unchanged, err := store.Load(ctx)
			if err != nil || !unchanged.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatal("unchanged metadata update time changed")
			}
			putSyncFile(t, srcDir, "file", "updated contents")
			changedAt := time.Now()
			run()
			after, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if after.UpdatedAt.Before(changedAt) || after.UpdatedAt.After(time.Now()) || after.UpdatedAt.Location() != time.UTC {
				t.Fatalf("changed metadata did not receive a new UTC update time: %v", after.UpdatedAt)
			}
			if after.Revision == before.Revision || store.saves != 2 || after.Version != 1 {
				t.Fatal("changed metadata did not receive a new revision")
			}
		})
	}
}

func TestSyncTypeConflictPolicies(t *testing.T) {
	for _, sourceDir := range []bool{false, true} {
		for _, policy := range []syncer.ConflictPolicy{syncer.ConflictAbort, syncer.ConflictSkip, syncer.ConflictPreferSrc, syncer.ConflictPreferDst} {
			t.Run(fmt.Sprintf("sourceDir=%t/policy=%d", sourceDir, policy), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				sourcePath, targetPath := "conflict", "conflict/child"
				if sourceDir {
					sourcePath, targetPath = targetPath, sourcePath
				}
				putSyncFile(t, srcDir, sourcePath, "source")
				putSyncFile(t, dstDir, targetPath, "target")
				putSyncFile(t, srcDir, "a-first", "copied")
				putSyncFile(t, dstDir, "conflict-neighbor", "neighbor")
				calls := 0
				ctx := context.WithValue(context.Background(), struct{}{}, "conflict-context")
				option := syncer.Option{Delete: true, OnConflict: func(callbackCtx context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
					calls++
					if callbackCtx != ctx || conflict.Source.Path() != "conflict" || conflict.Target.Path() != "conflict" || conflict.Source.IsDir() != sourceDir || conflict.Target.IsDir() == sourceDir || conflict.SourceState != nil || conflict.TargetState != nil {
						t.Fatalf("unexpected conflict: %+v", conflict)
					}
					return policy, nil
				}}
				err := syncer.Sync(ctx, src, dst, option)
				if calls != 1 {
					t.Fatalf("conflict calls=%d", calls)
				}
				wantSyncFile(t, srcDir, sourcePath, "source")
				if policy == syncer.ConflictAbort {
					if !errors.Is(err, syncer.ErrSyncConflict) {
						t.Fatalf("got %v", err)
					}
					wantSyncAbsent(t, dstDir, "a-first")
				} else {
					if err != nil {
						t.Fatal(err)
					}
					wantSyncFile(t, dstDir, "a-first", "copied")
					wantSyncAbsent(t, dstDir, "conflict-neighbor")
				}
				if policy == syncer.ConflictPreferSrc {
					wantSyncFile(t, dstDir, sourcePath, "source")
				} else {
					wantSyncFile(t, dstDir, targetPath, "target")
				}
			})
		}
	}
}

func TestSyncTypeConflictHandlerErrorsBeforeWrites(t *testing.T) {
	for _, invalidPolicy := range []bool{false, true} {
		t.Run(fmt.Sprint(invalidPolicy), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "a-first", "must not copy")
			putSyncFile(t, srcDir, "conflict", "source")
			putSyncFile(t, dstDir, "conflict/child", "target")
			err := syncer.Sync(context.Background(), src, dst, syncer.Option{OnConflict: func(context.Context, syncer.Conflict) (syncer.ConflictPolicy, error) {
				if invalidPolicy {
					return syncer.ConflictPolicy(255), nil
				}
				return syncer.ConflictPreferSrc, errSyncTest
			}})
			if err == nil || (!invalidPolicy && !errors.Is(err, errSyncTest)) {
				t.Fatalf("got %v", err)
			}
			wantSyncAbsent(t, dstDir, "a-first")
			wantSyncFile(t, dstDir, "conflict/child", "target")
		})
	}
}

func TestSyncTypeReplacementSkippedRemovalBlocksCreation(t *testing.T) {
	for _, sourceDir := range []bool{false, true} {
		t.Run(fmt.Sprint(sourceDir), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			sourcePath, targetPath := "conflict", "conflict/child"
			if sourceDir {
				sourcePath, targetPath = targetPath, sourcePath
			}
			putSyncFile(t, srcDir, sourcePath, "source")
			putSyncFile(t, dstDir, targetPath, "target")
			putSyncFile(t, srcDir, "z-other", "unrelated")
			store := &singleSyncStore{}
			err := syncer.Sync(context.Background(), src, &fileOperationFaultFS{FS: dst}, syncer.Option{
				OnConflict: syncer.PreferSrc(), StateStore: store,
				OnError: func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
					return syncer.ErrorSkip, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			wantSyncFile(t, dstDir, targetPath, "target")
			wantSyncFile(t, dstDir, "z-other", "unrelated")
			state, err := store.Load(context.Background())
			if err != nil || state == nil || !state.Valid() || state.Target["conflict"] != nil {
				t.Fatalf("metadata=%+v, err=%v", state, err)
			}
		})
	}
}

func TestSyncTypeConflictMetadata(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip=%t", skip), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			ctx := context.Background()
			store := &singleSyncStore{}
			option := syncer.Option{StateStore: store}
			putSyncFile(t, srcDir, "conflict/child", "original")
			if err := syncer.Sync(ctx, src, dst, option); err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), store.data...)
			removeSyncPath(t, srcDir, "conflict")
			putSyncFile(t, srcDir, "conflict", "replacement")
			option.OnConflict = func(_ context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
				if conflict.Target.IsDir() && (conflict.SourceState == nil || !conflict.SourceState.IsDir() || conflict.TargetState == nil || !conflict.TargetState.IsDir()) {
					t.Fatal("missing previous conflict snapshots")
				}
				if skip {
					return syncer.ConflictSkip, nil
				}
				return syncer.ConflictPreferSrc, nil
			}
			err := syncer.Sync(ctx, &sourceReadFault{FS: src, remainingFailures: 1}, dst, option)
			if skip {
				if err != nil || string(store.data) != string(before) {
					t.Fatalf("skipped metadata changed: %v", err)
				}
				wantSyncFile(t, dstDir, "conflict/child", "original")
				return
			}
			if !errors.Is(err, errSyncTest) {
				t.Fatalf("got %v", err)
			}
			state, err := store.Load(ctx)
			if err != nil || !state.Valid() || state.Target["conflict"] != nil || state.Target["conflict/child"] != nil || state.Source["conflict"] == nil || !state.Source["conflict"].IsDir {
				t.Fatalf("metadata=%+v, err=%v", state, err)
			}
			if err := syncer.Sync(ctx, src, dst, option); err != nil {
				t.Fatal(err)
			}
			wantSyncFile(t, dstDir, "conflict", "replacement")
		})
	}
}

type directoryRemovalFS struct {
	cloudfs.FS
	paths []string
}

func (f *directoryRemovalFS) Remove(ctx context.Context, path string) error {
	f.paths = append(f.paths, path)
	return f.FS.Remove(ctx, path)
}

func TestDirectoryRemovalUsesOneOperation(t *testing.T) {
	for _, mode := range []string{"sync-delete", "sync-replace", "bisync-delete", "bisync-replace", "bisync-delete-reverse", "bisync-replace-reverse"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			ctx := context.Background()
			store := &singleSyncStore{}
			syncOption := syncer.Option{Delete: true, StateStore: store, OnConflict: syncer.PreferSrc()}
			biOption := syncer.Option{
				Mode:       syncer.ModeTwoWay,
				StateStore: store, OnConflict: syncer.PreferSrc()}
			isBi := mode != "sync-delete" && mode != "sync-replace"
			reverse := mode == "bisync-delete-reverse" || mode == "bisync-replace-reverse"
			replace := mode == "sync-replace" || mode == "bisync-replace" || mode == "bisync-replace-reverse"
			putSyncFile(t, srcDir, "dir/nested/file", "contents")
			if isBi {
				if err := syncer.Sync(ctx, src, dst, biOption); err != nil {
					t.Fatal(err)
				}
			} else if err := syncer.Sync(ctx, src, dst, syncOption); err != nil {
				t.Fatal(err)
			}
			changedDir := srcDir
			if reverse {
				changedDir = dstDir
				biOption.OnConflict = syncer.PreferDst()
			}
			removeSyncPath(t, changedDir, "dir")
			if replace {
				putSyncFile(t, changedDir, "dir", "replacement")
			}
			source, target := &directoryRemovalFS{FS: src}, &directoryRemovalFS{FS: dst}
			var err error
			if isBi {
				err = syncer.Sync(ctx, source, target, biOption)
			} else {
				err = syncer.Sync(ctx, source, target, syncOption)
			}
			if err != nil {
				t.Fatal(err)
			}
			receiver, sender := target, source
			receiverDir := dstDir
			if reverse {
				receiver, sender = source, target
				receiverDir = srcDir
			}
			if !reflect.DeepEqual(receiver.paths, []string{"/dir"}) || len(sender.paths) != 0 {
				t.Fatalf("receiver removals=%v, sender removals=%v", receiver.paths, sender.paths)
			}
			if replace {
				wantSyncFile(t, receiverDir, "dir", "replacement")
			} else {
				wantSyncAbsent(t, receiverDir, "dir")
			}
			state, err := store.Load(ctx)
			if err != nil || state == nil || !state.Valid() {
				t.Fatalf("metadata=%+v, err=%v", state, err)
			}
			for _, records := range []map[string]*syncer.Record{state.Source, state.Target} {
				if records["dir/nested"] != nil || records["dir/nested/file"] != nil {
					t.Fatalf("stale directory metadata: %+v", records)
				}
			}
		})
	}
}

func TestSyncFileConflictPolicies(t *testing.T) {
	for _, policy := range []syncer.ConflictPolicy{syncer.ConflictAbort, syncer.ConflictSkip, syncer.ConflictPreferSrc, syncer.ConflictPreferDst} {
		for _, callback := range []bool{false, true} {
			t.Run(fmt.Sprintf("policy=%d/callback=%t", policy, callback), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				putSyncFile(t, srcDir, "a-first", "new file")
				putSyncFile(t, srcDir, "conflict", "source content")
				putSyncFile(t, dstDir, "conflict", "target")
				option := syncer.Option{OnConflict: fixedConflict(policy)}
				calls := 0
				if callback {
					option.OnConflict = func(_ context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
						calls++
						if conflict.Source.Path() != "conflict" || conflict.Target.Path() != "conflict" || conflict.Source.IsDir() || conflict.Target.IsDir() || conflict.Source.Size() != int64(len("source content")) || conflict.Target.Size() != int64(len("target")) {
							t.Fatalf("unexpected conflict: %+v", conflict)
						}
						return policy, nil
					}
				}
				err := syncer.Sync(context.Background(), src, dst, option)
				if callback && calls != 1 {
					t.Fatalf("conflict calls=%d", calls)
				}
				if policy == syncer.ConflictAbort {
					if !errors.Is(err, syncer.ErrSyncConflict) {
						t.Fatalf("got %v", err)
					}
					wantSyncAbsent(t, dstDir, "a-first")
				} else {
					if err != nil {
						t.Fatal(err)
					}
					wantSyncFile(t, dstDir, "a-first", "new file")
				}
				want := "target"
				if policy == syncer.ConflictPreferSrc {
					want = "source content"
				}
				wantSyncFile(t, dstDir, "conflict", want)
				wantSyncFile(t, srcDir, "conflict", "source content")
			})
		}
	}
}

func TestSyncFileConflictPreservesMetadata(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	store := &singleSyncStore{}
	putSyncFile(t, srcDir, "file", "original")
	option := syncer.Option{StateStore: store}
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), store.data...)
	calls := 0
	option.OnConflict = func(_ context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
		calls++
		if conflict.SourceState == nil || conflict.TargetState == nil || conflict.SourceState.Size() != int64(len("original")) || conflict.TargetState.Size() != int64(len("original")) {
			t.Fatal("missing previous file snapshots")
		}
		if conflict.SourceState.Path() != "file" || conflict.TargetState.Path() != "file" {
			t.Fatal("previous file snapshots must use relative paths")
		}
		return syncer.ConflictSkip, nil
	}
	if err := syncer.Sync(ctx, src, dst, option); err != nil || calls != 0 {
		t.Fatalf("unchanged files: err=%v calls=%d", err, calls)
	}
	putSyncFile(t, dstDir, "file", "independent target change")
	if err := syncer.Sync(ctx, src, dst, option); err != nil || calls != 1 || string(store.data) != string(before) {
		t.Fatalf("skipped conflict: err=%v calls=%d", err, calls)
	}
	wantSyncFile(t, dstDir, "file", "independent target change")
}
