package syncer

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
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
	if err := os.WriteFile(filepath.Join(dstDir, "file"), []byte("old target"), 0600); err != nil {
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
			HashAlgorithm: crypto.SHA256,
			Version:       1,
			Source:        make(map[string]*Record),
			Target:        make(map[string]*Record),
		},
		srcFS: &syncFS{algorithm: crypto.SHA256, FS: src}, dstFS: &syncFS{algorithm: crypto.SHA256, FS: dst},
		option: Option{Delete: true, StateStore: store, OnConflict: PreferSrc()},
	}
	if err := worker.init(ctx); err != nil {
		t.Fatal(err)
	}
	worker.srcTree, err = worker.srcFS.scan(ctx, worker.state.Source)
	if err != nil {
		t.Fatal(err)
	}
	if worker.srcTree.get("/missing") != nil {
		t.Fatal("missing path returned an entry")
	}
	if worker.srcTree.get("/file") == worker.srcTree.get("/other") {
		t.Fatal("different paths share the same scanned entry")
	}
	worker.dstTree, err = worker.dstFS.scan(ctx, worker.state.Target)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := func() []byte {
		t.Helper()
		source, target := make(map[string]*Record), make(map[string]*Record)
		for path, info := range worker.srcTree.entries {
			source[path] = info.toRecord()
		}
		for path, info := range worker.dstTree.entries {
			target[path] = info.toRecord()
		}
		data, err := json.Marshal(struct {
			Source, Target map[string]*Record
			State          *State
		}{source, target, worker.state})
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
		HashAlgorithm: worker.state.HashAlgorithm,
		Version:       worker.state.Version,
		Source:        maps.Clone(worker.state.Source),
		Target:        maps.Clone(worker.state.Target),
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
	if worker.srcTree.get("/file").hash != "" {
		t.Fatal("recording the transfer hash modified the scanned entry")
	}
	if saved.Source["file"].Hash == "" || saved.Source["file"].Hash != saved.Target["file"].Hash {
		t.Fatal("transfer hash was not confirmed on both sides")
	}
	if saved.Target["file"].Size != int64(len("source")) {
		t.Fatal("the scan replaced the confirmed target record")
	}
	if saved.Target["file"] == saved.Source["file"] {
		t.Fatal("transfer result reused the source record")
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

func TestRecordEqualityUsesAvailableHashes(t *testing.T) {
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
			source := &Record{Size: 3, ModTime: timestamp, Hash: test.sourceHash}
			target := &Record{Size: test.targetSize, ModTime: test.targetTime, Hash: test.targetHash}
			if equal := source.Equal(target); equal != test.equal {
				t.Fatalf("equal=%v want=%v", equal, test.equal)
			}
		})
	}
}

func TestSyncTreeWalkSubtree(t *testing.T) {
	tree := stateTree(map[string]*Record{
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
				walk = func(fn func(string, *entry) error) error { return tree.walkSubtree(test.path, fn) }
			}
			var got []string
			if err := walk(func(path string, visited *entry) error {
				if visited != tree.get(path) {
					t.Fatal("traversal did not retain the scanned entry")
				}
				got = append(got, path)
				return nil
			}); err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("paths=%v want=%v err=%v", got, test.want, err)
			}
			for _, stopErr := range []error{fs.SkipAll, errors.New("stop walk")} {
				got = nil
				err := walk(func(path string, _ *entry) error {
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
	if err := stateTree(nil).walk(func(path string, _ *entry) error {
		t.Fatalf("empty snapshot visited %q", path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncTreeWalkSkipDir(t *testing.T) {
	tree := stateTree(map[string]*Record{
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
			fn := func(path string, _ *entry) error {
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

func TestEntryChangesFromPreviousRecord(t *testing.T) {
	for _, test := range []struct {
		name      string
		cur, prev *Record
		changed   bool
	}{
		{"added", &Record{}, nil, true},
		{"unchanged file", &Record{Size: 1}, &Record{Size: 1}, false},
		{"changed file", &Record{Size: 2}, &Record{Size: 1}, true},
		{"unchanged directory", &Record{IsDir: true}, &Record{IsDir: true}, false},
		{"type changed", &Record{IsDir: true}, &Record{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := test.cur.entry("/file")
			current.record = test.prev
			if got := current.isChanged(); got != test.changed {
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

type pipelineFS struct {
	cloudfs.FS
	t        *testing.T
	planning bool
	reads    int
	writes   int
}

func (fsys *pipelineFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	if !fsys.planning {
		fsys.t.Fatal("scan opened file contents")
	}
	fsys.reads++
	return fsys.FS.Open(ctx, path)
}

func (fsys *pipelineFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	fsys.writes++
	return fsys.FS.Create(ctx, path)
}

func TestSyncPipelinePlansConcreteEndpoints(t *testing.T) {
	ctx := context.Background()
	var roots [2]string
	var filesystems [2]*syncFS
	var trees [2]*syncTree
	var observed [2]*pipelineFS
	for side := range roots {
		roots[side] = t.TempDir()
		fsys, err := local.New(&local.Option{Path: roots[side]})
		if err != nil {
			t.Fatal(err)
		}
		defer fsys.Close()
		observed[side] = &pipelineFS{FS: fsys, t: t}
		filesystems[side] = &syncFS{algorithm: crypto.SHA256, FS: observed[side]}
	}
	if err := os.WriteFile(filepath.Join(roots[0], "file"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	for side := range filesystems {
		var err error
		trees[side], err = filesystems[side].scan(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		observed[side].planning = true
	}
	worker := &syncer{
		srcFS:   filesystems[0],
		dstFS:   filesystems[1],
		srcTree: trees[0],
		dstTree: trees[1],
		state:   &State{Source: make(map[string]*Record), Target: make(map[string]*Record)},
		option:  Option{Checksum: true},
	}
	tasks, err := worker.oneWayTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for side, fsys := range observed {
		if fsys.writes != 0 || fsys.reads != 0 {
			t.Fatal("new file planning performed I/O")
		}
		if scanned := trees[side].entries["/file"]; scanned != nil && (scanned.hash != "" || trees[side].get("/file") != scanned) {
			t.Fatal("planning changed the scanned entry")
		}
	}
	if len(tasks) != 1 || tasks[0].op != OpCopy || tasks[0].path != "/file" {
		t.Fatal("unexpected copy plan")
	}
	exec := &executor{
		srcFS: filesystems[0],
		dstFS: filesystems[1],
		option: Option{
			Concurrency: 2,
		},
		state: &State{
			Source: make(map[string]*Record),
			Target: make(map[string]*Record),
		},
	}
	if err := exec.run(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	if observed[0].reads+observed[1].reads != 1 {
		t.Fatal("new file was read more than once for transfer")
	}
	for _, root := range roots {
		data, err := os.ReadFile(filepath.Join(root, "file"))
		if err != nil || string(data) != "source" {
			t.Fatalf("unexpected final content: %q, %v", data, err)
		}
	}
}

func TestScanExcludesFilteredPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "excluded"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"file", "excluded/child", "excluded-file"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := local.New(&local.Option{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	observed := &pipelineFS{FS: backend, t: t}
	fsys := &syncFS{
		algorithm: crypto.SHA256,
		FS:        observed,
		option: Option{Skipper: func(info FileInfo) bool {
			return info.Path() == "/excluded" || info.Path() == "/excluded-file"
		}},
	}
	tree, err := fsys.scan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.entries) != 1 || !tree.skipped["/excluded"] || !tree.skipped["/excluded-file"] || tree.get("/file").hash != "" {
		t.Fatalf("unexpected scan result: %+v", tree)
	}
	if observed.writes != 0 || observed.reads != 0 {
		t.Fatal("scan read contents or wrote files")
	}
}

func TestEntryHashIsLazyAndCached(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reuse=%t", reuse), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "file"), []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			backend, err := local.New(&local.Option{Path: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			observed := &pipelineFS{FS: backend, t: t}
			fsys := &syncFS{
				FS:        observed,
				algorithm: crypto.SHA256,
			}
			ctx := context.Background()
			hash := crypto.SHA256.New()
			hash.Write([]byte("data"))
			want := fmt.Sprintf("%x", hash.Sum(nil))
			var records map[string]*Record
			if reuse {
				info, err := backend.Stat(ctx, "/file")
				if err != nil {
					t.Fatal(err)
				}
				records = map[string]*Record{
					"/file": {Size: info.Size(), ModTime: info.ModTime().UTC(), Hash: want},
				}
			}
			tree, err := fsys.scan(ctx, records)
			if err != nil {
				t.Fatal(err)
			}
			scanned := tree.get("/file")
			snapshot := scanned.FileInfo()
			if snapshot.Hash() != "" || observed.reads != 0 {
				t.Fatal("scan or snapshot capture fetched a hash")
			}
			observed.planning = true
			for range 2 {
				if err := scanned.loadHash(ctx); err != nil {
					t.Fatal(err)
				}
				if got := scanned.Hash(); got != want {
					t.Fatalf("hash=%q, want %q", got, want)
				}
			}
			wantReads := 1
			if reuse {
				wantReads = 0
			}
			if observed.reads != wantReads {
				t.Fatalf("reads=%d, want %d", observed.reads, wantReads)
			}
			if snapshot.Hash() != "" || scanned.toRecord().Hash != want || reuse && records["/file"].Hash != want {
				t.Fatal("lazy hashing changed a retained snapshot or lost the cached hash")
			}
		})
	}
}
