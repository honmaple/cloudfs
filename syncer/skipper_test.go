package syncer_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

func TestSkipper(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			store := &singleSyncStore{}
			run := func(skip syncer.Skipper) error {
				if mode == "sync" {
					return syncer.Sync(context.Background(), src, dst, syncer.Option{Delete: true, StateStore: store, OnConflict: syncer.PreferSrc(), Skipper: skip})
				}
				return syncer.Sync(context.Background(), src, dst, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: store, OnConflict: syncer.PreferSrc(), Skipper: skip})
			}
			putSyncFile(t, srcDir, "existing/secret", "old")
			putSyncFile(t, srcDir, "gone", "old")
			if err := run(nil); err != nil {
				t.Fatal(err)
			}
			before, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			putSyncFile(t, srcDir, "existing/secret", "changed")
			putSyncFile(t, srcDir, "new/secret", "hidden")
			putSyncFile(t, srcDir, "new/public", "visible")
			putSyncFile(t, srcDir, "ignored/child", "hidden")
			putSyncFile(t, srcDir, "ignored-neighbor", "visible")
			removeSyncPath(t, srcDir, "gone")
			removeSyncPath(t, dstDir, "gone")
			skip := func(info syncer.FileInfo) bool {
				if info.Path() == "/gone" {
					t.Fatal("filter received a historical entry absent from both scans")
				}
				return strings.HasSuffix(info.Path(), "/secret") || info.Path() == "/ignored"
			}
			if err := run(skip); err != nil {
				t.Fatal(err)
			}
			wantSyncFile(t, dstDir, "existing/secret", "old")
			wantSyncAbsent(t, dstDir, "new/secret")
			wantSyncAbsent(t, dstDir, "ignored")
			wantSyncFile(t, dstDir, "new/public", "visible")
			wantSyncFile(t, dstDir, "ignored-neighbor", "visible")
			after, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !after.Valid() {
				t.Fatal("invalid metadata")
			}
			if !reflect.DeepEqual(before.Source["existing/secret"], after.Source["existing/secret"]) ||
				!reflect.DeepEqual(before.Target["existing/secret"], after.Target["existing/secret"]) {
				t.Fatal("excluded metadata changed")
			}
			if after.Source["gone"] != nil || after.Target["gone"] != nil {
				t.Fatal("entries absent from both scans retained their history")
			}
		})
	}
}

func TestSkipperProtectsAncestorOperations(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		for _, replacement := range []bool{false, true} {
			name := mode + "/delete"
			if replacement {
				name = mode + "/replace"
			}
			t.Run(name, func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				store := &singleSyncStore{}
				putSyncFile(t, srcDir, "dir/secret", "keep")
				putSyncFile(t, srcDir, "dir/public", "keep subtree")
				if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: store}); err != nil {
					t.Fatal(err)
				}
				if mode == "sync" {
					store = &singleSyncStore{}
				}
				left, right := src, dst
				from, to := srcDir, dstDir
				if mode == "reverse" {
					left, right = dst, src
					from, to = dstDir, srcDir
				}
				removeSyncPath(t, from, "dir")
				if replacement {
					putSyncFile(t, from, "dir", "replacement")
				}
				skip := func(info syncer.FileInfo) bool { return info.Path() == "/dir/secret" }
				var err error
				if mode == "sync" {
					err = syncer.Sync(context.Background(), left, right, syncer.Option{Delete: true, Skipper: skip, OnConflict: syncer.PreferSrc()})
				} else {
					err = syncer.Sync(context.Background(), left, right, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						StateStore: store, Skipper: skip, OnConflict: syncer.PreferSrc()})
				}
				if err != nil {
					t.Fatal(err)
				}
				wantSyncFile(t, to, "dir/secret", "keep")
				wantSyncFile(t, to, "dir/public", "keep subtree")
			})
		}
	}
}

func TestSkipperOneWayDirectoryOperations(t *testing.T) {
	for _, operation := range []string{"copy-directory", "replace-directory", "delete-directory"} {
		t.Run(operation, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			switch operation {
			case "copy-directory":
				putSyncFile(t, srcDir, "dir/secret", "excluded")
				putSyncFile(t, srcDir, "dir/public", "copy")
				putSyncFile(t, dstDir, "dir", "replace")
			case "replace-directory":
				putSyncFile(t, srcDir, "dir", "replacement")
				fallthrough
			case "delete-directory":
				putSyncFile(t, dstDir, "dir/secret", "keep")
				putSyncFile(t, dstDir, "dir/public", "keep subtree")
			}
			if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
				Delete:     true,
				OnConflict: syncer.PreferSrc(),
				Skipper: func(info syncer.FileInfo) bool {
					return info.Path() == "/dir/secret"
				},
			}); err != nil {
				t.Fatal(err)
			}
			if operation == "copy-directory" {
				wantSyncFile(t, dstDir, "dir/public", "copy")
				wantSyncAbsent(t, dstDir, "dir/secret")
				wantSyncFile(t, srcDir, "dir/secret", "excluded")
			} else {
				wantSyncFile(t, dstDir, "dir/secret", "keep")
				wantSyncFile(t, dstDir, "dir/public", "keep subtree")
			}
		})
	}
}

func TestSkipperEitherSide(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "short")
	putSyncFile(t, dstDir, "file", "long contents")
	skip := func(info syncer.FileInfo) bool { return info.Size() > 10 }
	if err := syncer.Sync(context.Background(), src, dst, syncer.Option{Skipper: skip}); err != nil {
		t.Fatal(err)
	}
	wantSyncFile(t, dstDir, "file", "long contents")
}

func TestSkipperCancellation(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "data")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var target cloudfs.FS = &denySyncWrites{FS: dst}
	err := syncer.Sync(ctx, src, target, syncer.Option{Skipper: func(syncer.FileInfo) bool { cancel(); return false }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestSkipperPrunesScanBeforeChecksum(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			for _, root := range []string{srcDir, dstDir} {
				putSyncFile(t, root, "ignored/child", "hidden")
				putSyncFile(t, root, "secret", "hidden")
			}
			putSyncFile(t, srcDir, "public", "visible")
			source, target := &excludedIOFS{FS: src, t: t}, &excludedIOFS{FS: dst, t: t}
			skip := func(info syncer.FileInfo) bool {
				if strings.HasPrefix(info.Path(), "/ignored/") {
					t.Fatalf("visited excluded descendant %s", info.Path())
				}
				if info.Hash() != "" {
					t.Fatal("filter ran after hashing")
				}
				return info.Path() == "/ignored" || info.Path() == "/secret"
			}
			var err error
			if mode == "sync" {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{Checksum: true, Skipper: skip})
			} else {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Mode:     syncer.ModeTwoWay,
					Checksum: true, Skipper: skip, StateStore: &singleSyncStore{}})
			}
			if err != nil {
				t.Fatal(err)
			}
			wantSyncFile(t, dstDir, "public", "visible")
		})
	}
}

type excludedIOFS struct {
	cloudfs.FS
	t *testing.T
}

func (f *excludedIOFS) List(ctx context.Context, path string) ([]cloudfs.FileInfo, error) {
	if path == "/ignored" || strings.HasPrefix(path, "/ignored/") {
		f.t.Fatalf("listed excluded directory %s", path)
	}
	return f.FS.List(ctx, path)
}
func (f *excludedIOFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	if path == "/secret" || strings.HasPrefix(path, "/ignored/") {
		f.t.Fatalf("opened excluded file %s", path)
	}
	return f.FS.Open(ctx, path)
}

func TestSkipperIgnoresMissingSideHistory(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			store := &singleSyncStore{}
			run := func(skip syncer.Skipper) error {
				if mode == "sync" {
					return syncer.Sync(context.Background(), src, dst, syncer.Option{
						StateStore: store,
						Delete:     true,
						Skipper:    skip,
						OnConflict: syncer.PreferSrc(),
					})
				}
				return syncer.Sync(context.Background(), src, dst, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: store,
					Skipper:    skip,
					OnConflict: syncer.PreferSrc(),
				})
			}
			putSyncFile(t, srcDir, "file", "long historical content")
			if err := run(nil); err != nil {
				t.Fatal(err)
			}
			removeSyncPath(t, srcDir, "file")
			putSyncFile(t, dstDir, "file", "small")
			calls := 0
			if err := run(func(info syncer.FileInfo) bool {
				calls++
				if info.Size() != 5 || info.Hash() != "" {
					t.Fatal("filter received historical metadata")
				}
				return info.Size() > 10
			}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("filter calls=%d, want one current entry", calls)
			}
			wantSyncAbsent(t, srcDir, "file")
			wantSyncAbsent(t, dstDir, "file")
			state, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if state.Source["file"] != nil || state.Target["file"] != nil {
				t.Fatal("confirmed deletion retained its baseline")
			}
		})
	}
}

func TestSkipperRetainsPrunedHistoryUntilIncluded(t *testing.T) {
	for _, deleteBoth := range []bool{false, true} {
		name := "one side deleted"
		if deleteBoth {
			name = "both sides deleted"
		}
		t.Run(name, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			store := &singleSyncStore{}
			run := func(skip syncer.Skipper) error {
				return syncer.Sync(context.Background(), src, dst, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: store,
					Skipper:    skip,
				})
			}
			putSyncFile(t, srcDir, "dir/file", "baseline")
			if err := run(nil); err != nil {
				t.Fatal(err)
			}
			before, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			removeSyncPath(t, srcDir, "dir/file")
			if deleteBoth {
				removeSyncPath(t, dstDir, "dir/file")
			}
			if err := run(func(info syncer.FileInfo) bool {
				if info.Path() != "/dir" {
					t.Fatalf("filter received a pruned descendant: %s", info.Path())
				}
				return true
			}); err != nil {
				t.Fatal(err)
			}
			after, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Source["dir/file"], after.Source["dir/file"]) ||
				!reflect.DeepEqual(before.Target["dir/file"], after.Target["dir/file"]) {
				t.Fatal("pruned descendant lost its baseline")
			}
			if err := run(nil); err != nil {
				t.Fatal(err)
			}
			wantSyncAbsent(t, srcDir, "dir/file")
			wantSyncAbsent(t, dstDir, "dir/file")
			after, err = store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if after.Source["dir/file"] != nil || after.Target["dir/file"] != nil {
				t.Fatal("included deletion retained its baseline")
			}
		})
	}
}

func TestSkipperPrunesOtherSideTraversal(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "dir/source", "source")
	putSyncFile(t, dstDir, "dir/target", "target")
	directories := 0
	if err := syncer.Sync(context.Background(), src, &denySyncWrites{FS: dst}, syncer.Option{
		Checksum: true,
		Delete:   true,
		Skipper: func(info syncer.FileInfo) bool {
			if info.Path() == "/dir" {
				directories++
				// Only the source scan prunes this directory. The target scan
				// contains children, but planning must still prune both sides.
				return directories == 1
			}
			return false
		},
	}); err != nil {
		t.Fatal(err)
	}
	if directories != 2 {
		t.Fatalf("directory callbacks=%d, want 2", directories)
	}
	wantSyncFile(t, srcDir, "dir/source", "source")
	wantSyncFile(t, dstDir, "dir/target", "target")
}
