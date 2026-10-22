package syncer_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

func TestBiSyncKeepsBothVersions(t *testing.T) {
	for _, kind := range []string{"files", "source-directory", "target-directory", "source-deleted", "target-deleted"} {
		t.Run(kind, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "dir/report.txt", "baseline")
			store := &singleSyncStore{}
			option := syncer.Option{
				Mode:         syncer.ModeTwoWay,
				Concurrency:  3,
				StateStore:   store,
				HashOnChange: true,
				OnConflict:   syncer.KeepBoth(),
			}
			ctx := context.Background()
			if err := syncer.Sync(ctx, src, dst, option); err != nil {
				t.Fatal(err)
			}
			putSyncFile(t, srcDir, "dir/report.txt", "source version")
			putSyncFile(t, dstDir, "dir/report.txt", "target version with more bytes")
			original, backup := "source version", "target version with more bytes"
			switch kind {
			case "source-directory":
				removeSyncPath(t, srcDir, "dir/report.txt")
				putSyncFile(t, srcDir, "dir/report.txt/child", "directory contents")
			case "target-directory":
				removeSyncPath(t, dstDir, "dir/report.txt")
				putSyncFile(t, dstDir, "dir/report.txt/child", "directory contents")
				backup = "source version"
			case "source-deleted":
				removeSyncPath(t, srcDir, "dir/report.txt")
				original = backup
			case "target-deleted":
				removeSyncPath(t, dstDir, "dir/report.txt")
			}
			before := []map[string]string{biSyncSubtree(t, srcDir, "."), biSyncSubtree(t, dstDir, ".")}
			previous, saves := bytes.Clone(store.data), store.saves
			var planned, executed []taskEventSnapshot
			option.DryRun = true
			option.OnEvent = taskEvents(func(notification syncer.Event) {
				event, ok := notification.(syncer.SyncEvent)
				if !ok || event.Status != syncer.StatusPlanned {
					t.Fatalf("unexpected dry-run event: %+v", notification)
				}
				snapshot := snapshotTaskEvent(event)
				snapshot.Status = syncer.StatusStarted
				planned = append(planned, snapshot)
			})
			if err := syncer.Sync(ctx, src, dst, option); err != nil {
				t.Fatal(err)
			}
			if got := []map[string]string{biSyncSubtree(t, srcDir, "."), biSyncSubtree(t, dstDir, ".")}; !reflect.DeepEqual(got, before) || store.saves != saves || !bytes.Equal(previous, store.data) {
				t.Fatal("preview changed files or state")
			}
			option.DryRun = false
			var active syncer.Op
			option.OnEvent = taskEvents(func(notification syncer.Event) {
				if event, ok := notification.(syncer.ProgressEvent); ok {
					if active != syncer.OpCopy {
						t.Fatal("non-copy operation emitted progress")
					}
					info := event.FileInfo
					if info == nil || info.IsDir() || event.Written > info.Size() {
						t.Fatalf("incorrect copy progress source: %+v", event)
					}
				}
				if event, ok := notification.(syncer.SyncEvent); ok && event.Status == syncer.StatusStarted {
					active = event.Op
					executed = append(executed, snapshotTaskEvent(event))
				}
			})
			if err := syncer.Sync(ctx, src, dst, option); err != nil {
				t.Fatal(err)
			}
			// Copies in the same stage may start in either order.
			for _, events := range [][]taskEventSnapshot{planned, executed} {
				sort.SliceStable(events, func(i, j int) bool {
					if events[i].Op != events[j].Op {
						return events[i].Op > events[j].Op
					}
					return events[i].Path < events[j].Path
				})
			}
			if !reflect.DeepEqual(planned, executed) {
				t.Fatalf("planned=%v executed=%v", planned, executed)
			}
			if kind == "files" {
				want := []taskEventSnapshot{
					{
						Path:    "/dir/report.txt",
						Op:      syncer.OpRename,
						NewPath: "/dir/report.conflict.txt",
						Status:  syncer.StatusStarted,
					},
					{
						Path:    "/dir/report.conflict.txt",
						Op:      syncer.OpCopy,
						Reverse: true,
						Status:  syncer.StatusStarted,
					},
					{
						Path:   "/dir/report.txt",
						Op:     syncer.OpCopy,
						Status: syncer.StatusStarted,
					},
				}
				if !reflect.DeepEqual(executed, want) {
					t.Fatalf("KeepBoth sequence=%+v, want %+v", executed, want)
				}
			}
			for _, root := range []string{srcDir, dstDir} {
				if kind == "source-directory" || kind == "target-directory" {
					wantSyncFile(t, root, "dir/report.txt/child", "directory contents")
				} else {
					wantSyncFile(t, root, "dir/report.txt", original)
				}
				if kind == "source-deleted" || kind == "target-deleted" {
					wantSyncAbsent(t, root, "dir/report.conflict.txt")
				} else {
					wantSyncFile(t, root, "dir/report.conflict.txt", backup)
				}
			}
			state, err := store.Load(ctx)
			if err != nil || !state.Valid() {
				t.Fatalf("invalid state: %v", err)
			}
			option.OnConflict = syncer.Abort()
			option.OnEvent = taskEvents(func(notification syncer.Event) {
				t.Fatalf("resolved conflict executed again: %+v", notification)
			})
			saves = store.saves
			if err := syncer.Sync(ctx, src, dst, option); err != nil || store.saves != saves {
				t.Fatalf("repeated run: %v, saves=%d", err, store.saves)
			}
		})
	}
}

func TestKeepBothBackupFailures(t *testing.T) {
	for _, renameFailure := range []bool{false, true} {
		for _, ignored := range []bool{false, true} {
			t.Run(fmt.Sprintf("rename=%t/ignored=%t", renameFailure, ignored), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				putSyncFile(t, srcDir, "file", "source version")
				putSyncFile(t, dstDir, "file", "target version with more bytes")
				store := &singleSyncStore{}
				left, right := src, dst
				if renameFailure {
					right = &conflictRenameFailureFS{FS: dst}
				} else {
					left = &conflictCopyFailureFS{FS: src, path: "/file.conflict"}
				}
				option := syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: store,
					OnConflict: syncer.KeepBoth(),
				}
				if ignored {
					option.OnError = func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
						wantOp, wantPath, wantNewPath := syncer.OpCopy, "/file.conflict", ""
						if renameFailure {
							wantOp, wantPath, wantNewPath = syncer.OpRename, "/file", "/file.conflict"
						}
						if failure.Op != wantOp || failure.FileInfo.Path() != wantPath || failure.NewPath != wantNewPath || failure.Reverse == renameFailure || failure.Attempt != 1 {
							t.Fatalf("incorrect backup error context: %+v", failure)
						}
						return syncer.ErrorSkip, nil
					}
				}
				err := syncer.Sync(context.Background(), left, right, option)
				if ignored && err != nil || !ignored && !errors.Is(err, errSyncTest) {
					t.Fatalf("got %v", err)
				}
				wantSyncFile(t, srcDir, "file", "source version")
				if renameFailure {
					wantSyncFile(t, dstDir, "file", "target version with more bytes")
					wantSyncAbsent(t, dstDir, "file.conflict")
					if store.saves != 0 {
						t.Fatal("failed rename saved state")
					}
				} else {
					if ignored {
						wantSyncFile(t, dstDir, "file", "source version")
					} else {
						wantSyncAbsent(t, dstDir, "file")
					}
					wantSyncFile(t, dstDir, "file.conflict", "target version with more bytes")
					state, err := store.Load(context.Background())
					if err != nil || state == nil || (state.Target["file"] != nil) != ignored || state.Target["file.conflict"] == nil || state.Source["file.conflict"] != nil {
						t.Fatalf("invalid rename checkpoint: %+v, %v", state, err)
					}
				}
				option.OnError = nil
				if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
					t.Fatal(err)
				}
				wantSyncFile(t, srcDir, "file", "source version")
				wantSyncFile(t, dstDir, "file", "source version")
				for _, root := range []string{srcDir, dstDir} {
					wantSyncFile(t, root, "file.conflict", "target version with more bytes")
				}
			})
		}
	}
}

func TestKeepBothAvoidsExistingNames(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "source version")
	putSyncFile(t, dstDir, "file", "target version")
	putSyncFile(t, srcDir, "file.conflict", "existing source file")
	putSyncFile(t, dstDir, "file.conflict-2", "existing target file")
	if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{},
		OnConflict: syncer.KeepBoth(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "file.conflict", "existing source file")
		wantSyncFile(t, root, "file.conflict-2", "existing target file")
		wantSyncFile(t, root, "file.conflict-3", "target version")
	}
}

func TestKeepBothAvoidsFilteredExistingName(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "source version")
	putSyncFile(t, dstDir, "file", "target version")
	putSyncFile(t, srcDir, "file.conflict", "excluded file")
	if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{},
		OnConflict: syncer.KeepBoth(),
		Skipper: func(info syncer.FileInfo) bool {
			return info.Path() == "/file.conflict"
		},
	}); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, srcDir, "file.conflict", "excluded file")
	wantSyncAbsent(t, dstDir, "file.conflict")
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "file.conflict-2", "target version")
	}
}

func TestKeepBothContinuesAfterSkippedRename(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	for _, path := range []string{"a", "b"} {
		putSyncFile(t, srcDir, path, "source "+path)
		putSyncFile(t, dstDir, path, "target version "+path)
	}
	store := &singleSyncStore{}
	if err := syncer.Sync(context.Background(), src, &conflictRenameFailureFS{FS: dst, path: "/a"}, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store,
		OnConflict: syncer.KeepBoth(),
		OnError: func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
			return syncer.ErrorSkip, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, srcDir, "a", "source a")
	wantSyncFile(t, dstDir, "a", "target version a")
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "b", "source b")
		wantSyncFile(t, root, "b.conflict", "target version b")
	}
	state, err := store.Load(context.Background())
	if err != nil || state == nil || !state.Valid() {
		t.Fatalf("invalid partial state: %+v, %v", state, err)
	}
	if state.Source["a.conflict"] != nil || state.Target["a.conflict"] != nil ||
		state.Source["b.conflict"] == nil || state.Target["b.conflict"] == nil {
		t.Fatalf("incorrect backup state: %+v", state)
	}
}

func TestKeepBothRespectsFilter(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "source version")
	putSyncFile(t, dstDir, "file", "target version")
	store := &singleSyncStore{}
	err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store,
		OnConflict: syncer.KeepBoth(),
		Skipper: func(info syncer.FileInfo) bool {
			if info.Hash() != "" {
				t.Fatal("filter received a content hash")
			}
			return info.Path() == "/file.conflict"
		},
	})
	if !errors.Is(err, syncer.ErrSyncConflict) || store.saves != 0 {
		t.Fatalf("err=%v saves=%d", err, store.saves)
	}
	wantSyncFile(t, srcDir, "file", "source version")
	wantSyncFile(t, dstDir, "file", "target version")
	wantSyncAbsent(t, srcDir, "file.conflict")
	wantSyncAbsent(t, dstDir, "file.conflict")
}

func TestKeepBothRetriesPartialBackup(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "source version")
	putSyncFile(t, dstDir, "file", "target version")
	calls := 0
	err := syncer.Sync(context.Background(), src, &sourceReadFault{
		FS:                dst,
		remainingFailures: 1,
	}, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{},
		OnConflict: syncer.KeepBoth(),
		OnError: func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
			calls++
			return syncer.ErrorRetry, nil
		},
	})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "file", "source version")
		wantSyncFile(t, root, "file.conflict", "target version")
	}
}

func TestKeepBothCancellationSavesCompletedOperations(t *testing.T) {
	for _, after := range []syncer.Op{syncer.OpRename, syncer.OpCopy} {
		t.Run(after.String(), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "source version")
			putSyncFile(t, dstDir, "file", "target version")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &singleSyncStore{}
			option := syncer.Option{
				Mode:       syncer.ModeTwoWay,
				StateStore: store,
				OnConflict: syncer.KeepBoth(),
				OnEvent: taskEvents(func(notification syncer.Event) {
					if event, ok := notification.(syncer.SyncEvent); ok && event.Op == after && (after == syncer.OpRename || event.FileInfo.Path() == "/file.conflict") && event.Status == syncer.StatusSucceeded {
						cancel()
					}
				}),
			}
			if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
			wantSyncFile(t, srcDir, "file", "source version")
			wantSyncAbsent(t, dstDir, "file")
			wantSyncFile(t, dstDir, "file.conflict", "target version")
			state, err := store.Load(context.Background())
			if err != nil || state == nil || !state.Valid() || state.Target["file.conflict"] == nil || state.Source["file"] != nil || state.Target["file"] != nil {
				t.Fatalf("invalid rename checkpoint: %+v, %v", state, err)
			}
			if (state.Source["file.conflict"] != nil) != (after == syncer.OpCopy) {
				t.Fatalf("invalid copy checkpoint: %+v", state)
			}
			option.OnEvent = nil
			if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{srcDir, dstDir} {
				wantSyncFile(t, root, "file", "source version")
				wantSyncFile(t, root, "file.conflict", "target version")
				wantSyncAbsent(t, root, "file.conflict-2")
			}
		})
	}
}

func TestSyncRejectsKeepBoth(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "source")
	putSyncFile(t, dstDir, "file", "target with different size")
	for _, callback := range []bool{false, true} {
		option := syncer.Option{
			OnConflict: syncer.KeepBoth(),
		}
		if callback {
			option.OnConflict = func(context.Context, syncer.Conflict) (syncer.ConflictPolicy, error) {
				return syncer.ConflictKeepBoth, nil
			}
		}
		if err := syncer.Sync(context.Background(), src, dst, option); !errors.Is(err, syncer.ErrSyncConflict) {
			t.Fatalf("got %v", err)
		}
	}
	wantSyncFile(t, srcDir, "file", "source")
	wantSyncFile(t, dstDir, "file", "target with different size")
}

func TestKeepBothChecksRenamedSourceContent(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "source version")
	putSyncFile(t, dstDir, "file", "target version")
	info, err := os.Stat(filepath.Join(dstDir, "file"))
	if err != nil {
		t.Fatal(err)
	}
	store := &singleSyncStore{}
	err = syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store,
		OnConflict: syncer.KeepBoth(),
		Checksum:   true,
		OnEvent: taskEvents(func(notification syncer.Event) {
			if event, ok := notification.(syncer.SyncEvent); ok && event.NewPath != "" && event.Op == syncer.OpRename && event.Status == syncer.StatusSucceeded {
				putSyncFile(t, dstDir, "file.conflict", "TARGET VERSION")
				if err := os.Chtimes(filepath.Join(dstDir, "file.conflict"), info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
		}),
	})
	if !errors.Is(err, syncer.ErrSourceChanged) {
		t.Fatalf("err=%v saves=%d", err, store.saves)
	}
	wantSyncFile(t, srcDir, "file", "source version")
	wantSyncAbsent(t, dstDir, "file")
	wantSyncFile(t, dstDir, "file.conflict", "TARGET VERSION")
}

func TestKeepBothRandomConvergence(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			random := rand.New(rand.NewSource(seed))
			option := syncer.Option{
				Mode:         syncer.ModeTwoWay,
				Concurrency:  3,
				StateStore:   &singleSyncStore{},
				HashOnChange: true,
				OnConflict:   syncer.KeepBoth(),
			}
			contents := func(root string) map[string]bool {
				result := make(map[string]bool)
				for _, content := range biSyncSubtree(t, root, ".") {
					if content != "<directory>" {
						result[content] = true
					}
				}
				return result
			}
			for round := 0; round < 6; round++ {
				before := contents(srcDir)
				fresh := make(map[string]bool)
				for _, root := range []string{srcDir, dstDir} {
					for edit, count := 0, 1+random.Intn(4); edit < count; edit++ {
						mutateSyncTree(t, random, root, seed, round, edit)
					}
					for content := range contents(root) {
						if !before[content] {
							fresh[content] = true
						}
					}
				}
				option.OnEvent = nil
				if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
					t.Fatalf("round %d: %v", round, err)
				}
				if !reflect.DeepEqual(biSyncSubtree(t, srcDir, "."), biSyncSubtree(t, dstDir, ".")) {
					t.Fatalf("round %d: trees did not converge", round)
				}
				after := contents(srcDir)
				for content := range fresh {
					if !after[content] {
						t.Fatalf("round %d: fresh content lost: %q", round, content)
					}
				}
				option.OnEvent = taskEvents(func(event syncer.Event) {
					t.Fatalf("round %d: repeated run executed a task: %+v", round, event)
				})
				if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
					t.Fatalf("round %d: repeated run: %v", round, err)
				}
			}
		})
	}
}

func TestKeepBothRenameDoesNotCountAsDeletion(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "path", "source file")
	putSyncFile(t, dstDir, "path/child", "target directory")
	store := &singleSyncStore{}
	option := syncer.Option{
		Mode:             syncer.ModeTwoWay,
		StateStore:       store,
		OnConflict:       syncer.KeepBoth(),
		MaxDeletePercent: 99,
	}
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{srcDir, dstDir} {
		wantSyncFile(t, root, "path.conflict", "source file")
		wantSyncFile(t, root, "path/child", "target directory")
	}
}

type conflictCopyFailureFS struct {
	cloudfs.FS
	path string
}

func (fsys *conflictCopyFailureFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	if path == fsys.path {
		return nil, errSyncTest
	}
	return fsys.FS.Create(ctx, path)
}

// Rename failures occur before any backup content is copied.
type conflictRenameFailureFS struct {
	cloudfs.FS
	path string
}

func (fsys *conflictRenameFailureFS) Rename(ctx context.Context, path, name string) error {
	if fsys.path == "" || fsys.path == path {
		return errSyncTest
	}
	return fsys.FS.Rename(ctx, path, name)
}

func TestKeepBothUsesRenamedSnapshot(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "dir/file", "source version")
			putSyncFile(t, dstDir, "dir/file", "target version")
			renameDir, preserved := dstDir, "target version"
			if reverse {
				removeSyncPath(t, dstDir, "dir/file")
				putSyncFile(t, dstDir, "dir/file/child", "directory contents")
				renameDir, preserved = srcDir, "source version"
			}
			before, err := os.Stat(filepath.Join(renameDir, "dir/file"))
			if err != nil {
				t.Fatal(err)
			}
			renamedTime := time.Unix(1700000000, 0).UTC()
			left, right := src, dst
			if reverse {
				left = &renameTimeFS{FS: src, root: srcDir, modTime: renamedTime}
			} else {
				right = &renameTimeFS{FS: dst, root: dstDir, modTime: renamedTime}
			}
			var retained syncer.FileInfo
			store := &singleSyncStore{}
			err = syncer.Sync(context.Background(), left, right, syncer.Option{
				Mode:        syncer.ModeTwoWay,
				Concurrency: 2,
				StateStore:  store,
				OnConflict:  syncer.KeepBoth(),
				OnEvent: taskEvents(func(notification syncer.Event) {
					if event, ok := notification.(syncer.SyncEvent); ok && event.Op == syncer.OpRename && event.Status == syncer.StatusStarted {
						retained = event.FileInfo
					}
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if retained == nil || !retained.ModTime().Equal(before.ModTime()) {
				t.Fatal("rename modified the retained scan snapshot")
			}
			for _, root := range []string{srcDir, dstDir} {
				wantSyncFile(t, root, "dir/file.conflict", preserved)
			}
			state, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			entries := state.Target
			if reverse {
				entries = state.Source
			}
			if !entries["dir/file.conflict"].ModTime.Equal(renamedTime) {
				t.Fatal("backup state did not retain the rename result")
			}
		})
	}
}

type renameTimeFS struct {
	cloudfs.FS
	root    string
	modTime time.Time
}

func (fsys *renameTimeFS) Rename(ctx context.Context, path, name string) error {
	if err := fsys.FS.Rename(ctx, path, name); err != nil {
		return err
	}
	newPath := filepath.Join(fsys.root, filepath.Dir(strings.TrimPrefix(path, "/")), name)
	return os.Chtimes(newPath, fsys.modTime, fsys.modTime)
}
