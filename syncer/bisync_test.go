package syncer_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/middleware"
	"github.com/honmaple/cloudfs/syncer"
)

func TestBiSyncMultipleRuns(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}}
	putSyncFile(t, srcDir, "left/file", "left")
	putSyncFile(t, dstDir, "right/file", "right")
	putSyncFile(t, srcDir, ".cloudfs-bisync.json", "gone")
	if err := os.Mkdir(filepath.Join(srcDir, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	run := func() {
		t.Helper()
		if err := syncer.Sync(ctx, src, dst, option); err != nil {
			t.Fatal(err)
		}
	}
	run()
	wantSyncFile(t, srcDir, "right/file", "right")
	wantSyncFile(t, dstDir, "left/file", "left")
	wantSyncFile(t, dstDir, ".cloudfs-bisync.json", "gone")
	if info, err := os.Stat(filepath.Join(dstDir, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory: %v", err)
	}
	putSyncFile(t, srcDir, "left/file", "new left")
	putSyncFile(t, dstDir, "right/file", "new right")
	run()
	wantSyncFile(t, dstDir, "left/file", "new left")
	wantSyncFile(t, srcDir, "right/file", "new right")
	removeSyncPath(t, srcDir, "left")
	removeSyncPath(t, dstDir, "right")
	removeSyncPath(t, srcDir, ".cloudfs-bisync.json")
	removeSyncPath(t, dstDir, ".cloudfs-bisync.json")
	run()
	wantSyncAbsent(t, dstDir, "left")
	wantSyncAbsent(t, srcDir, "right")
	run()
}

func TestBiSyncConflictsDoNotWrite(t *testing.T) {
	for _, kind := range []string{"edit-edit", "delete-edit", "delete-add", "type"} {
		t.Run(kind, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			ctx := context.Background()
			store := &singleSyncStore{}
			option := syncer.Option{
				Mode:       syncer.ModeTwoWay,
				StateStore: store}
			putSyncFile(t, srcDir, "z/file", "initial")
			if err := syncer.Sync(ctx, src, dst, option); err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), store.data...)
			putSyncFile(t, srcDir, "a-first", "must not copy")
			switch kind {
			case "edit-edit":
				putSyncFile(t, srcDir, "z/file", "aaa")
				putSyncFile(t, dstDir, "z/file", "bbb")
			case "delete-edit":
				removeSyncPath(t, srcDir, "z")
				putSyncFile(t, dstDir, "z/file", "edited")
			case "delete-add":
				removeSyncPath(t, srcDir, "z")
				putSyncFile(t, dstDir, "z/added", "added")
			case "type":
				removeSyncPath(t, srcDir, "z")
				putSyncFile(t, srcDir, "z", "now a file")
			}
			if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, syncer.ErrSyncConflict) {
				t.Fatalf("got %v", err)
			}
			wantSyncAbsent(t, dstDir, "a-first")
			if !bytes.Equal(before, store.data) {
				t.Fatal("comparison error changed metadata")
			}
		})
	}
}

func TestBiSyncConflictPolicies(t *testing.T) {
	for _, policy := range []syncer.ConflictPolicy{syncer.ConflictPreferSrc, syncer.ConflictPreferDst} {
		for _, kind := range []string{"edit-edit", "delete-edit", "edit-delete", "delete-add", "add-delete", "file-dir", "dir-file"} {
			t.Run(fmt.Sprintf("%d/%s", policy, kind), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				ctx := context.Background()
				store := &singleSyncStore{}
				option := syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: store}
				putSyncFile(t, srcDir, "z/old", "initial")
				if err := syncer.Sync(ctx, src, dst, option); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "edit-edit":
					putSyncFile(t, srcDir, "z/old", "source")
					putSyncFile(t, dstDir, "z/old", "destination")
				case "delete-edit":
					removeSyncPath(t, srcDir, "z/old")
					putSyncFile(t, dstDir, "z/old", "edited")
				case "edit-delete":
					putSyncFile(t, srcDir, "z/old", "edited")
					removeSyncPath(t, dstDir, "z/old")
				case "delete-add":
					removeSyncPath(t, srcDir, "z")
					putSyncFile(t, dstDir, "z/new/child", "added")
				case "add-delete":
					putSyncFile(t, srcDir, "z/new/child", "added")
					removeSyncPath(t, dstDir, "z")
				case "file-dir":
					removeSyncPath(t, srcDir, "z")
					putSyncFile(t, srcDir, "z", "source file")
					putSyncFile(t, dstDir, "z/extra", "destination extra")
				case "dir-file":
					removeSyncPath(t, dstDir, "z")
					putSyncFile(t, dstDir, "z", "destination file")
					putSyncFile(t, srcDir, "z/extra", "source extra")
				}
				newDir := srcDir
				if policy == syncer.ConflictPreferDst {
					newDir = dstDir
				}
				expected := biSyncSubtree(t, newDir, "z")
				// A preference resolves conflicts only; independent edits still merge.
				putSyncFile(t, srcDir, "left-only", "left")
				putSyncFile(t, dstDir, "right-only", "right")
				option.OnConflict = fixedConflict(policy)
				if err := syncer.Sync(ctx, src, dst, option); err != nil {
					t.Fatal(err)
				}
				for _, root := range []string{srcDir, dstDir} {
					if actual := biSyncSubtree(t, root, "z"); !reflect.DeepEqual(actual, expected) {
						t.Fatalf("got %v, want %v", actual, expected)
					}
					wantSyncFile(t, root, "left-only", "left")
					wantSyncFile(t, root, "right-only", "right")
				}
				// A resolved conflict advances confirmed metadata.
				option.OnConflict = syncer.Abort()
				if err := syncer.Sync(ctx, src, dst, option); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func biSyncSubtree(t *testing.T, root, path string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	if _, err := os.Stat(filepath.Join(root, path)); errors.Is(err, fs.ErrNotExist) {
		return result
	}
	err := filepath.WalkDir(filepath.Join(root, path), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			result[rel] = "<directory>"
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		result[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type closeFailureFS struct{ cloudfs.FS }

type closeFailureWriter struct{ cloudfs.FileWriter }

func (w closeFailureWriter) Close() error {
	return errors.Join(w.FileWriter.Close(), errSyncTest)
}

func (f closeFailureFS) Create(ctx context.Context, p string) (cloudfs.FileWriter, error) {
	w, err := f.FS.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	return closeFailureWriter{w}, nil
}

type opaqueMissingFS struct{ cloudfs.FS }

func (f opaqueMissingFS) Open(ctx context.Context, p string) (cloudfs.File, error) {
	r, err := f.FS.Open(ctx, p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("backend: object not found")
	}
	return r, err
}

func TestBiSyncFirstRunConflictPolicies(t *testing.T) {
	for _, policy := range []syncer.ConflictPolicy{syncer.ConflictAbort, syncer.ConflictSkip, syncer.ConflictPreferSrc, syncer.ConflictPreferDst} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "z", "source")
			putSyncFile(t, dstDir, "z/child", "destination")
			newDir := srcDir
			if policy == syncer.ConflictPreferDst {
				newDir = dstDir
			}
			expected := biSyncSubtree(t, newDir, "z")
			store := &singleSyncStore{}
			err := syncer.Sync(context.Background(), src, dst, syncer.Option{
				Mode:       syncer.ModeTwoWay,
				StateStore: store, OnConflict: fixedConflict(policy)})
			if policy == syncer.ConflictAbort || policy == syncer.ConflictSkip {
				if policy == syncer.ConflictAbort && !errors.Is(err, syncer.ErrSyncConflict) {
					t.Fatalf("got %v", err)
				}
				if policy == syncer.ConflictSkip && err != nil {
					t.Fatal(err)
				}
				wantSyncFile(t, srcDir, "z", "source")
				wantSyncFile(t, dstDir, "z/child", "destination")
				if store.saves != 0 {
					t.Fatal("unconfirmed conflict was saved")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{srcDir, dstDir} {
				if actual := biSyncSubtree(t, root, "z"); !reflect.DeepEqual(actual, expected) {
					t.Fatalf("got %v want %v", actual, expected)
				}
			}
		})
	}
}

func TestBiSyncConflictSkipPreservesSubtreeMetadata(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	store := &singleSyncStore{}
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store}
	putSyncFile(t, srcDir, "z/old", "initial")
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removeSyncPath(t, srcDir, "z")
	putSyncFile(t, dstDir, "z/new", "edited")
	putSyncFile(t, srcDir, "independent", "copied")
	var conflicts []string
	option.OnConflict = func(_ context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
		conflicts = append(conflicts, conflict.Target.Path())
		return syncer.ConflictSkip, nil
	}
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conflicts, []string{"/z"}) {
		t.Fatalf("conflicts=%v", conflicts)
	}
	after, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"z", "z/old"} {
		if !reflect.DeepEqual(before.Source[path], after.Source[path]) || !reflect.DeepEqual(before.Target[path], after.Target[path]) {
			t.Fatalf("skipped metadata advanced: %s", path)
		}
	}
	if after.Target["z/new"] != nil {
		t.Fatal("skipped child advanced metadata")
	}
	wantSyncAbsent(t, srcDir, "z")
	wantSyncFile(t, dstDir, "independent", "copied")
	option.OnConflict = nil
	if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, syncer.ErrSyncConflict) {
		t.Fatalf("skip hid conflict: %v", err)
	}
}

func TestBiSyncCustomConflictHandler(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}}
	putSyncFile(t, srcDir, "deleted", "original")
	putSyncFile(t, srcDir, "edited", "original")
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	removeSyncPath(t, srcDir, "deleted")
	putSyncFile(t, dstDir, "deleted", "destination")
	putSyncFile(t, srcDir, "edited", "source")
	putSyncFile(t, dstDir, "edited", "destination")
	called := make(map[string]bool)
	option.OnConflict = func(ctx context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
		if err := ctx.Err(); err != nil {
			return syncer.ConflictAbort, err
		}
		if conflict.SourceState == nil || conflict.TargetState == nil || conflict.SourceState.Size() != int64(len("original")) {
			t.Fatalf("missing metadata: %+v", conflict)
		}
		info := conflict.Source
		if info == nil {
			info = conflict.Target
		}
		called[info.Path()] = true
		if info.Path() == "/deleted" {
			if conflict.Source != nil || conflict.Target == nil {
				t.Fatalf("invalid deletion: %+v", conflict)
			}
			return syncer.ConflictPreferDst, nil
		}
		if conflict.Source == nil || conflict.Target == nil {
			t.Fatalf("invalid edit: %+v", conflict)
		}
		return syncer.ConflictPreferSrc, nil
	}
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	if len(called) != 2 {
		t.Fatalf("called=%v", called)
	}
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "deleted", "destination")
		wantSyncFile(t, root, "edited", "source")
	}
	option.OnConflict = nil
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
}

func TestBiSyncResolverFailureDoesNotWrite(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "a-first", "pending")
			putSyncFile(t, srcDir, "conflict", "source")
			putSyncFile(t, dstDir, "conflict", "destination")
			store := &singleSyncStore{}
			err := syncer.Sync(context.Background(), src, dst, syncer.Option{
				Mode:       syncer.ModeTwoWay,
				StateStore: store, OnConflict: func(context.Context, syncer.Conflict) (syncer.ConflictPolicy, error) {
					if invalid {
						return syncer.ConflictPolicy(255), nil
					}
					return syncer.ConflictPreferSrc, errSyncTest
				}})
			if err == nil || !invalid && !errors.Is(err, errSyncTest) {
				t.Fatalf("got %v", err)
			}
			wantSyncAbsent(t, dstDir, "a-first")
			if store.saves != 0 {
				t.Fatal("resolver failure saved metadata")
			}
		})
	}
}

func TestBiSyncPrefixAndCancellation(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "input/file", "scoped")
	putSyncFile(t, dstDir, "output/keep", "keep")
	src, err := cloudfs.New(src, middleware.PrefixFS("/input"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err = cloudfs.New(dst, middleware.PrefixFS("/output"))
	if err != nil {
		t.Fatal(err)
	}
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}}
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "output/file", "scoped")
	wantSyncFile(t, srcDir, "input/keep", "keep")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestBiSyncScansOnceWithoutDownloadingUnchangedFiles(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "nested/left", "left")
	putSyncFile(t, dstDir, "nested/right", "right")
	source := &observedSyncFS{FS: src}
	target := &observedSyncFS{FS: dst, timeOffset: 24 * time.Hour}
	store := &singleSyncStore{}
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store}
	ctx := context.Background()
	if err := syncer.Sync(ctx, source, target, option); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 1 || target.dataOpens != 1 {
		t.Fatalf("initial opens=%d/%d", source.dataOpens, target.dataOpens)
	}
	source.rejectData, target.rejectData = true, true
	if err := syncer.Sync(ctx, source, target, option); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 1 || target.dataOpens != 1 || store.saves != 1 {
		t.Fatalf("noop: opens=%d/%d saves=%d", source.dataOpens, target.dataOpens, store.saves)
	}
	for _, side := range []*observedSyncFS{source, target} {
		if !reflect.DeepEqual(side.lists, map[string]int{"/": 2, "/nested": 2}) {
			t.Fatalf("rescanned tree: %v", side.lists)
		}
	}
}

func TestBiSyncFreshComparisonAfterPartialFailure(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "a-finished", "first")
	putSyncFile(t, srcDir, "z-failed", "second file")
	source := &observedSyncFS{FS: src}
	fault := &pathReadFault{FS: source, path: "/z-failed"}
	store := &singleSyncStore{}
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store, OnConflict: syncer.PreferSrc()}
	ctx := context.Background()
	if err := syncer.Sync(ctx, fault, dst, option); !errors.Is(err, errSyncTest) {
		t.Fatalf("got %v", err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.Source["a-finished"] == nil || state.Target["z-failed"] != nil {
		t.Fatal("incorrect partial metadata")
	}
	putSyncFile(t, srcDir, "z-failed", "changed source after failure")
	if err := syncer.Sync(ctx, fault, dst, option); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 3 || source.lists["/"] != 2 {
		t.Fatalf("opens=%d scans=%v", source.dataOpens, source.lists)
	}
	wantSyncFile(t, dstDir, "z-failed", "changed source after failure")
}

func TestBiSyncOnErrorRetriesBothDirectionsWithContext(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "left", "source contents")
	putSyncFile(t, dstDir, "right", "target contents")
	source, target := &operationContextFS{FS: src}, &operationContextFS{FS: dst}
	ctx := context.Background()
	calls := make(map[string]int)
	err := syncer.Sync(ctx, &sourceReadFault{FS: source, remainingFailures: 1}, &sourceReadFault{FS: target, remainingFailures: 1}, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}, OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
			path := failure.FileInfo.Path()
			calls[path]++
			return syncer.ErrorRetry, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, map[string]int{"/left": 1, "/right": 1}) {
		t.Fatalf("callbacks=%v", calls)
	}
	if source.openCtx != ctx || target.openCtx != ctx || source.createCtx != ctx || target.createCtx != ctx {
		t.Fatal("synchronization context was not used for retries")
	}
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "left", "source contents")
		wantSyncFile(t, root, "right", "target contents")
	}
}

func TestBiSyncIgnoredReplacementBlocksDependentTasks(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	store := &singleSyncStore{}
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store}
	putSyncFile(t, srcDir, "z/old", "old")
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	removeSyncPath(t, srcDir, "z")
	putSyncFile(t, srcDir, "z", "source file")
	putSyncFile(t, srcDir, "zz-independent", "continued")
	target := &fileOperationFaultFS{FS: dst}
	option.OnConflict = syncer.PreferSrc()
	option.OnError = func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		return syncer.ErrorSkip, nil
	}
	if err := syncer.Sync(ctx, src, target, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "z/old", "old")
	wantSyncFile(t, dstDir, "zz-independent", "continued")
	option.OnError = nil
	if err := syncer.Sync(ctx, src, target, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "z", "source file")
}

func TestBiSyncPartialReplacementSavesConfirmedRemoval(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}}
	putSyncFile(t, srcDir, "z", "old file")
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	removeSyncPath(t, srcDir, "z")
	putSyncFile(t, srcDir, "z/child", "new child")
	target := &fileOperationFaultFS{FS: dst, removeFailed: true}
	option.OnConflict = syncer.PreferSrc()
	option.OnError = func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
		return syncer.ErrorSkip, nil
	}
	if err := syncer.Sync(ctx, src, target, option); err != nil {
		t.Fatal(err)
	}
	wantSyncAbsent(t, dstDir, "z")
	option.OnError, option.OnConflict = nil, syncer.Abort()
	if err := syncer.Sync(ctx, src, target, option); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "z/child", "new child")
}

func TestSyncReportsTransferCloseErrors(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		for _, action := range []string{"fail", "skip", "retry"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				if mode == "reverse" {
					src, dst, srcDir, dstDir = dst, src, dstDir, srcDir
				}
				putSyncFile(t, srcDir, "file", "data")
				store := &singleSyncStore{}
				ctx := context.Background()
				fault := &onceCloseFailureFS{FS: dst}
				calls := 0
				handler := func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
					calls++
					if !errors.Is(failure.Err, errSyncTest) || failure.Err.Error() != errSyncTest.Error() {
						t.Fatalf("unexpected close error: %v", failure.Err)
					}
					switch action {
					case "skip":
						return syncer.ErrorSkip, nil
					case "retry":
						return syncer.ErrorRetry, nil
					default:
						return syncer.ErrorAbort, failure.Err
					}
				}
				var err error
				if mode == "sync" {
					err = syncer.Sync(ctx, src, fault, syncer.Option{StateStore: store, OnError: handler})
				} else if mode == "reverse" {
					err = syncer.Sync(ctx, fault, src, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						StateStore: store, OnError: handler})
				} else {
					err = syncer.Sync(ctx, src, fault, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						StateStore: store, OnError: handler})
				}
				if calls != 1 {
					t.Fatalf("handler calls=%d", calls)
				}
				if action == "fail" {
					if !errors.Is(err, errSyncTest) || err.Error() != errSyncTest.Error() {
						t.Fatalf("unexpected error prefix: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				wantSyncFile(t, dstDir, "file", "data")
				if action == "retry" {
					if store.saves != 1 || fault.creates != 2 {
						t.Fatalf("saves=%d creates=%d", store.saves, fault.creates)
					}
				} else if store.saves != 0 {
					t.Fatal("unconfirmed transfer saved metadata")
				}
			})
		}
	}
}

type onceCloseFailureFS struct {
	cloudfs.FS
	creates int
}

func (f *onceCloseFailureFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	w, err := f.FS.Create(ctx, path)
	if err != nil {
		return nil, err
	}
	f.creates++
	if f.creates == 1 {
		return closeFailureWriter{w}, nil
	}
	return w, nil
}

func TestBiSyncBackendMissingError(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "data")
	if err := syncer.Sync(context.Background(), opaqueMissingFS{src}, opaqueMissingFS{dst}, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}}); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "file", "data")
}

func TestBiSyncIgnoredCopyRetainsPreviousMetadata(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			if reverse {
				src, dst, srcDir, dstDir = dst, src, dstDir, srcDir
			}
			ctx := context.Background()
			store := &singleSyncStore{}
			option := syncer.Option{
				Mode:       syncer.ModeTwoWay,
				StateStore: store}
			putSyncFile(t, srcDir, "z-failed", "old contents")
			var err error
			if reverse {
				err = syncer.Sync(ctx, dst, src, option)
			} else {
				err = syncer.Sync(ctx, src, dst, option)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			putSyncFile(t, srcDir, "z-failed", "changed source contents")
			putSyncFile(t, dstDir, "a-independent", "other direction")
			fault := &pathReadFault{FS: src, path: "/z-failed"}
			option.OnError = func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
				path := failure.FileInfo.Path()
				if path != "/z-failed" {
					t.Fatalf("unexpected failure: %s: %v", path, failure.Err)
				}
				return syncer.ErrorSkip, nil
			}
			if reverse {
				err = syncer.Sync(ctx, dst, fault, option)
			} else {
				err = syncer.Sync(ctx, fault, dst, option)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Source["z-failed"], after.Source["z-failed"]) || !reflect.DeepEqual(before.Target["z-failed"], after.Target["z-failed"]) {
				t.Fatal("ignored copy advanced its metadata")
			}
			wantSyncFile(t, srcDir, "a-independent", "other direction")
			if after.Source["a-independent"] == nil || after.Target["a-independent"] == nil {
				t.Fatal("independent success was not recorded")
			}
		})
	}
}

func TestBiSyncReplacementKeepsNeighborPaths(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	ctx := context.Background()
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}}
	for _, path := range []string{"a/child", "a-b/child", "a.b/child", "a0/child"} {
		putSyncFile(t, srcDir, path, "original")
	}
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	removeSyncPath(t, srcDir, "a")
	putSyncFile(t, srcDir, "a", "replacement file")
	putSyncFile(t, dstDir, "a-b/child", "changed target")
	putSyncFile(t, srcDir, "a.b/child", "changed source")
	option.OnConflict = syncer.PreferSrc()
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "a", "replacement file")
		wantSyncFile(t, root, "a-b/child", "changed target")
		wantSyncFile(t, root, "a.b/child", "changed source")
		wantSyncFile(t, root, "a0/child", "original")
	}
}

// Both streams must close exactly once even when transfer and finalization fail.
func TestSyncJoinsTransferAndCloseErrors(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "data")
	readErr, sourceCloseErr, targetCloseErr := errors.New("read failed"), errors.New("source close failed"), errors.New("target close failed")
	source := &transferFailureFS{FS: src, readErr: readErr, closeErr: sourceCloseErr}
	target := &transferFailureFS{FS: dst, closeErr: targetCloseErr}
	err := syncer.Sync(context.Background(), source, target)
	for _, cause := range []error{readErr, sourceCloseErr, targetCloseErr} {
		if !errors.Is(err, cause) {
			t.Fatalf("missing %v in %v", cause, err)
		}
	}
	if source.closes != 1 || target.closes != 1 {
		t.Fatalf("close counts: %d %d", source.closes, target.closes)
	}
}

type transferFailureFS struct {
	cloudfs.FS
	readErr, closeErr error
	closes            int
}

func (f *transferFailureFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	r, err := f.FS.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return &transferFailureReader{File: r, owner: f}, nil
}
func (f *transferFailureFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	w, err := f.FS.Create(ctx, path)
	if err != nil {
		return nil, err
	}
	return &transferFailureWriter{FileWriter: w, owner: f}, nil
}

type transferFailureReader struct {
	cloudfs.File
	owner *transferFailureFS
}

func (r *transferFailureReader) Read(p []byte) (int, error) { return 0, r.owner.readErr }
func (r *transferFailureReader) Close() error {
	r.owner.closes++
	return errors.Join(r.File.Close(), r.owner.closeErr)
}

type transferFailureWriter struct {
	cloudfs.FileWriter
	owner *transferFailureFS
}

func (w *transferFailureWriter) Close() error {
	w.owner.closes++
	return errors.Join(w.FileWriter.Close(), w.owner.closeErr)
}
