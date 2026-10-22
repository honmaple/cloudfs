package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/driver/local"
)

func TestSyncPreservesSnapshotsDuringExecution(t *testing.T) {
	ctx := context.Background()
	srcDir, dstDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "other"), []byte("another file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "file"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "extra"), []byte("remove"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := local.New(&local.Option{Path: srcDir})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := local.New(&local.Option{Path: dstDir})
	if err != nil {
		t.Fatal(err)
	}
	store := &snapshotStore{}
	worker := &syncer{
		state: &State{
			Type:    "sync",
			Version: 1,
			Source:  make(map[string]*Entry),
			Target:  make(map[string]*Entry),
		},
		srcFS: &syncFS{FS: src}, dstFS: &syncFS{FS: dst},
		option: Option{Delete: true, StateStore: store},
	}
	if err := worker.init(ctx); err != nil {
		t.Fatal(err)
	}
	worker.srcTree, err = worker.srcFS.scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if worker.srcTree.get("/missing") != nil {
		t.Fatal("missing path returned an entry")
	}
	if worker.srcTree.get("/file") == worker.srcTree.get("/other") {
		t.Fatal("different paths share the same scanned entry")
	}
	worker.dstTree, err = worker.dstFS.scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := func() []byte {
		t.Helper()
		data, err := json.Marshal(struct {
			Source, Target map[string]*Entry
			State          *State
		}{worker.srcTree.entries, worker.dstTree.entries, worker.state})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := snapshots()
	tasks, err := worker.oneWayTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := &State{
		Type:    worker.state.Type,
		Version: worker.state.Version,
		Source:  maps.Clone(worker.state.Source),
		Target:  maps.Clone(worker.state.Target),
	}
	exec := &executor{
		srcFS:   worker.srcFS,
		dstFS:   worker.dstFS,
		option:  worker.option,
		state:   state,
		skipped: make(map[string]bool),
	}
	for _, task := range tasks {
		if err := exec.execute(ctx, task); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, snapshots()) {
			t.Fatal("execution modified a scanned snapshot or loaded metadata")
		}
	}
	if err := worker.finish(ctx, state); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, snapshots()) {
		t.Fatal("commit modified a scanned snapshot or loaded metadata")
	}
	saved := store.state
	if saved == nil || !saved.Valid() {
		t.Fatal("saved metadata must have valid relative paths")
	}
	if saved.Source["file"] == worker.srcTree.get("/file") || worker.srcTree.get("/file").Hash != "" {
		t.Fatal("recording the transfer hash modified the scanned entry")
	}
	if saved.Source["file"].Hash == "" || saved.Source["file"].Hash != saved.Target["file"].Hash {
		t.Fatal("transfer hash was not confirmed on both sides")
	}
	if saved.Target["file"] == saved.Source["file"] {
		t.Fatal("transfer result reused the source entry")
	}
	if _, exists := saved.Target["file"]; !exists {
		t.Fatal("new metadata omitted the copied file")
	}
	if _, exists := saved.Target["extra"]; exists {
		t.Fatal("new metadata retained the deleted file")
	}
}

// An in-memory implementation can retain State directly, without encoding.
type snapshotStore struct{ state *State }

func (store *snapshotStore) Load(context.Context) (*State, error) {
	return store.state, nil
}

func (store *snapshotStore) Save(_ context.Context, state *State) error {
	store.state = state
	return nil
}

func TestEntryEqualityUsesAvailableHashes(t *testing.T) {
	timestamp := time.Unix(1700000000, 0)
	for _, test := range []struct {
		name                   string
		sourceHash, targetHash string
		targetSize             int64
		targetTime             time.Time
		equal                  bool
	}{
		{"both hashes equal", "hash-a", "hash-a", 99, timestamp.Add(time.Hour), true},
		{"both hashes differ", "hash-a", "hash-b", 3, timestamp, false},
		{"source hash only", "hash-a", "", 3, timestamp, true},
		{"target hash only", "", "hash-b", 3, timestamp, true},
		{"no hashes", "", "", 3, timestamp, true},
		{"source hash with changed size", "hash-a", "", 4, timestamp, false},
		{"target hash with changed time", "", "hash-b", 3, timestamp.Add(time.Hour), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &Entry{Size: 3, ModTime: timestamp, Hash: test.sourceHash}
			target := &Entry{Size: test.targetSize, ModTime: test.targetTime, Hash: test.targetHash}
			if equal := source.Equal(target); equal != test.equal {
				t.Fatalf("equal=%v want=%v", equal, test.equal)
			}
		})
	}
}

func TestSyncTreeWalkSubtree(t *testing.T) {
	tree := newSyncTree(map[string]*Entry{
		"/a": {IsDir: true}, "/a/file": {Size: 1},
		"/a/nested": {IsDir: true}, "/a/nested/file": {Size: 2},
		"/a-b": {IsDir: true}, "/a-b/file": {Size: 3},
		"/a.b": {Size: 4}, "/a0": {Size: 5}, "/aa": {Size: 6},
		"/目录": {IsDir: true}, "/目录/文件": {Size: 7}, "/目录-旁支": {Size: 8},
	})
	for _, test := range []struct {
		path string
		want []string
	}{
		{"", []string{"/a", "/a/file", "/a/nested", "/a/nested/file", "/a-b", "/a-b/file", "/a.b", "/a0", "/aa", "/目录", "/目录/文件", "/目录-旁支"}},
		{"/a", []string{"/a", "/a/file", "/a/nested", "/a/nested/file"}},
		{"/a/nested", []string{"/a/nested", "/a/nested/file"}},
		{"/a/file", []string{"/a/file"}},
		{"/a/n", nil},
		{"/missing", nil},
		{"/目录", []string{"/目录", "/目录/文件"}},
	} {
		t.Run(test.path, func(t *testing.T) {
			walk := tree.walk
			if test.path != "" {
				walk = func(fn func(string, *Entry) error) error { return tree.walkSubtree(test.path, fn) }
			}
			var got []string
			if err := walk(func(path string, entry *Entry) error {
				if entry != tree.get(path) {
					t.Fatal("traversal did not retain the scanned entry")
				}
				got = append(got, path)
				return nil
			}); err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("paths=%v want=%v err=%v", got, test.want, err)
			}
			for _, stopErr := range []error{fs.SkipAll, errors.New("stop walk")} {
				got = nil
				err := walk(func(path string, _ *Entry) error {
					got = append(got, path)
					return stopErr
				})
				want := test.want
				if len(want) > 1 {
					want = want[:1]
				}
				wantErr := stopErr
				if stopErr == fs.SkipAll || len(want) == 0 {
					wantErr = nil
				}
				if !slices.Equal(got, want) || err != wantErr {
					t.Fatalf("early-stop paths=%v want=%v err=%v", got, want, err)
				}
			}
		})
	}
	if err := newSyncTree(nil).walk(func(path string, _ *Entry) error {
		t.Fatalf("empty snapshot visited %q", path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncTreeWalkExcludesSkipped(t *testing.T) {
	tree := newSyncTree(map[string]*Entry{
		"/included": {},
	})
	tree.skipped = map[string]bool{"/excluded-dir": true, "/excluded-file": true}
	for _, root := range []string{"", "/excluded-dir", "/excluded-file"} {
		t.Run(root, func(t *testing.T) {
			var paths []string
			visit := func(path string, _ *Entry) error {
				paths = append(paths, path)
				return nil
			}
			var err error
			if root == "" {
				err = tree.walk(visit)
			} else {
				err = tree.walkSubtree(root, visit)
			}
			var want []string
			if root == "" {
				want = []string{"/included"}
			}
			if err != nil || !slices.Equal(paths, want) {
				t.Fatalf("paths=%v want=%v err=%v", paths, want, err)
			}
		})
	}
}

func TestSyncTreeWalkSkipDir(t *testing.T) {
	tree := newSyncTree(map[string]*Entry{
		"/a": {IsDir: true}, "/a/child": {IsDir: true}, "/a/child/file": {},
		"/a/child-neighbor": {}, "/a/z": {},
		"/a-b": {IsDir: true}, "/a-b/file": {}, "/a.b": {}, "/b": {},
	})
	for _, test := range []struct {
		name, root string
		skip       map[string]bool
		want       []string
	}{
		{"directory", "", map[string]bool{"/a": true}, []string{"/a", "/a-b", "/a-b/file", "/a.b", "/b"}},
		{"neighbor directories", "", map[string]bool{"/a": true, "/a-b": true}, []string{"/a", "/a-b", "/a.b", "/b"}},
		{"nested directory", "", map[string]bool{"/a/child": true}, []string{"/a", "/a/child", "/a/child-neighbor", "/a/z", "/a-b", "/a-b/file", "/a.b", "/b"}},
		{"file skips remaining siblings", "", map[string]bool{"/a/child-neighbor": true}, []string{"/a", "/a/child", "/a/child/file", "/a/child-neighbor", "/a-b", "/a-b/file", "/a.b", "/b"}},
		{"top-level file", "", map[string]bool{"/a.b": true}, []string{"/a", "/a/child", "/a/child/file", "/a/child-neighbor", "/a/z", "/a-b", "/a-b/file", "/a.b"}},
		{"nested file keeps parent siblings", "", map[string]bool{"/a/child/file": true}, []string{"/a", "/a/child", "/a/child/file", "/a/child-neighbor", "/a/z", "/a-b", "/a-b/file", "/a.b", "/b"}},
		{"subtree file", "/a/child/file", map[string]bool{"/a/child/file": true}, []string{"/a/child/file"}},
		{"subtree root", "/a", map[string]bool{"/a": true}, []string{"/a"}},
		{"subtree child", "/a", map[string]bool{"/a/child": true}, []string{"/a", "/a/child", "/a/child-neighbor", "/a/z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			fn := func(path string, _ *Entry) error {
				got = append(got, path)
				if test.skip[path] {
					return fs.SkipDir
				}
				return nil
			}
			var err error
			if test.root == "" {
				err = tree.walk(fn)
			} else {
				err = tree.walkSubtree(test.root, fn)
			}
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("paths=%v want=%v err=%v", got, test.want, err)
			}
		})
	}
}

func TestSyncEntryChanges(t *testing.T) {
	worker := &syncer{}
	for _, test := range []struct {
		name      string
		cur, prev *Entry
		changed   bool
	}{
		{"both absent", nil, nil, false},
		{"added", &Entry{}, nil, true},
		{"deleted", nil, &Entry{}, true},
		{"unchanged file", &Entry{Size: 1}, &Entry{Size: 1}, false},
		{"changed file", &Entry{Size: 2}, &Entry{Size: 1}, true},
		{"unchanged directory", &Entry{IsDir: true}, &Entry{IsDir: true}, false},
		{"type changed", &Entry{IsDir: true}, &Entry{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := worker.isChanged(test.cur, test.prev); got != test.changed {
				t.Fatalf("changed=%v want=%v", got, test.changed)
			}
		})
	}
}

func TestSyncPreservesLoadedMetadata(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			srcDir, dstDir := t.TempDir(), t.TempDir()
			src, err := local.New(&local.Option{Path: srcDir})
			if err != nil {
				t.Fatal(err)
			}
			dst, err := local.New(&local.Option{Path: dstDir})
			if err != nil {
				t.Fatal(err)
			}
			store := &snapshotStore{}
			ctx := context.Background()
			run := func() {
				t.Helper()
				var err error
				if mode == "sync" {
					err = Sync(ctx, src, dst, Option{OnConflict: PreferSrc(), StateStore: store})
				} else {
					err = Sync(ctx, src, dst, Option{
						Mode:       ModeTwoWay,
						StateStore: store})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(srcDir, "file"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			run()
			previous := store.state
			before, err := json.Marshal(previous)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(srcDir, "file"), []byte("updated contents"), 0600); err != nil {
				t.Fatal(err)
			}
			run()
			after, err := json.Marshal(previous)
			if err != nil {
				t.Fatal(err)
			}
			if store.state == previous || !bytes.Equal(before, after) {
				t.Fatal("saving changed metadata modified the previously loaded snapshot")
			}
			confirmed := store.state
			run()
			if store.state != confirmed {
				t.Fatal("unchanged metadata was saved again")
			}
		})
	}
}
