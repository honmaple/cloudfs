package syncer_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/middleware"
	"github.com/honmaple/cloudfs/syncer"
)

func TestSyncReusesSavedHashes(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "data")
			putSyncFile(t, srcDir, "untouched", "keep")
			source, target := &observedSyncFS{FS: src}, &observedSyncFS{FS: dst}
			store := &singleSyncStore{}
			var tasks []taskEventSnapshot
			handler := taskEvents(func(notification syncer.Event) {
				var event taskEventSnapshot
				switch value := notification.(type) {
				case syncer.SyncEvent:
					event = snapshotTaskEvent(value)
				default:
					return
				}
				if event.Status == syncer.StatusStarted {
					tasks = append(tasks, event)
				}
			})
			run := func() {
				t.Helper()
				var err error
				if mode == "sync" {
					err = syncer.Sync(context.Background(), source, target, syncer.Option{
						Checksum:   true,
						StateStore: store,
						OnConflict: syncer.PreferSrc(),
						OnEvent:    handler,
					})
				} else {
					left, right := source, target
					if mode == "reverse" {
						left, right = target, source
					}
					err = syncer.Sync(context.Background(), left, right, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						Checksum:   true,
						StateStore: store,
						OnEvent:    handler,
					})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			run()
			if source.dataOpens != 2 || target.dataOpens != 0 || len(tasks) != 2 {
				t.Fatalf("baseline: reads=%d/%d tasks=%v", source.dataOpens, target.dataOpens, tasks)
			}
			baseline, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"file", "untouched"} {
				if baseline.Source[path].Hash == "" || baseline.Source[path].Hash != baseline.Target[path].Hash {
					t.Fatal("successful transfer did not confirm the hash on both sides")
				}
			}
			source.rejectData, target.rejectData = true, true
			tasks = nil
			saves := store.saves
			run()
			if len(tasks) != 0 || store.saves != saves {
				t.Fatal("unchanged files caused work")
			}
			// Only a timestamp changed: read that side once, refresh its stamp,
			// and preserve the receiver's content and modification time.
			info, err := os.Stat(filepath.Join(dstDir, "file"))
			if err != nil {
				t.Fatal(err)
			}
			stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := os.Chtimes(filepath.Join(srcDir, "file"), stamp, stamp); err != nil {
				t.Fatal(err)
			}
			source.rejectData = false
			run()
			if len(tasks) != 0 || source.dataOpens != 3 || target.dataOpens != 0 || store.saves != saves+1 {
				t.Fatalf("timestamp change: reads=%d/%d saves=%d tasks=%v", source.dataOpens, target.dataOpens, store.saves, tasks)
			}
			after, err := os.Stat(filepath.Join(dstDir, "file"))
			if err != nil || !after.ModTime().Equal(info.ModTime()) {
				t.Fatal("timestamp-only change rewrote the receiver")
			}
			source.rejectData = true
			run()
			if store.saves != saves+1 {
				t.Fatal("refreshed timestamp was not reused")
			}
			putSyncFile(t, srcDir, "file", "changed contents")
			source.rejectData = false
			run()
			if source.dataOpens != 5 || target.dataOpens != 0 || len(tasks) != 1 || tasks[0].Path != "/file" {
				t.Fatalf("content change: reads=%d/%d tasks=%v", source.dataOpens, target.dataOpens, tasks)
			}
			wantSyncFile(t, dstDir, "file", "changed contents")
		})
	}
}

func TestSyncWithStateRequiresChecksumForHashComparison(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "data")
	source, target := &observedSyncFS{FS: src}, &observedSyncFS{FS: dst}
	option := syncer.Option{StateStore: &singleSyncStore{}}
	ctx := context.Background()
	if err := syncer.Sync(ctx, source, target, option); err != nil {
		t.Fatal(err)
	}

	stamp := time.Unix(1000000000, 0)
	if err := os.Chtimes(filepath.Join(srcDir, "file"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	source.rejectData, target.rejectData = true, true
	if err := syncer.Sync(ctx, source, target, option); !errors.Is(err, syncer.ErrSyncConflict) {
		t.Fatalf("metadata comparison returned %v", err)
	}
	if source.dataOpens != 1 || target.dataOpens != 0 {
		t.Fatalf("metadata comparison opened files: %d/%d", source.dataOpens, target.dataOpens)
	}

	source.rejectData = false
	option.Checksum = true
	if err := syncer.Sync(ctx, source, target, option); err != nil {
		t.Fatal(err)
	}
	if source.dataOpens != 2 || target.dataOpens != 0 {
		t.Fatalf("checksum should hash the changed side and reuse the saved target hash: %d/%d", source.dataOpens, target.dataOpens)
	}
	wantSyncFile(t, dstDir, "file", "data")
}

func TestSyncFillsMissingHashesBeforeComparing(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprint(bisync), func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			putSyncFile(t, srcDir, "file", "data")
			store := &singleSyncStore{}
			run := func() error {
				if bisync {
					return syncer.Sync(context.Background(), src, dst, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						Checksum:   true,
						StateStore: store,
					})
				}
				return syncer.Sync(context.Background(), src, dst, syncer.Option{
					Checksum:   true,
					StateStore: store,
				})
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			state, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// Simulate an older state with metadata only on both sides.
			state.Source["file"].Hash, state.Target["file"].Hash = "", ""
			if err := state.UpdateRevision(); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			putSyncFile(t, srcDir, "file", "DATA")
			stamp := state.Source["file"].ModTime
			if err := os.Chtimes(filepath.Join(srcDir, "file"), stamp, stamp); err != nil {
				t.Fatal(err)
			}
			previous, saves := bytes.Clone(store.data), store.saves
			if err := run(); !errors.Is(err, syncer.ErrSyncConflict) {
				t.Fatalf("newly discovered content mismatch was ignored: %v", err)
			}
			if store.saves != saves || !bytes.Equal(previous, store.data) {
				t.Fatal("unresolved mismatch advanced the baseline")
			}
		})
	}
}

func TestSyncHashesDistinguishTouchFromModification(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "dir/file", "data")
			option := syncer.Option{
				Mode:       syncer.ModeTwoWay,
				Checksum:   true,
				StateStore: &singleSyncStore{},
			}
			if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
				t.Fatal(err)
			}
			deleted, touched := srcDir, dstDir
			if reverse {
				deleted, touched = touched, deleted
			}
			removeSyncPath(t, deleted, "dir")
			stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := os.Chtimes(filepath.Join(touched, "dir/file"), stamp, stamp); err != nil {
				t.Fatal(err)
			}
			// A timestamp-only change must not turn deletion into delete/edit conflict.
			if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
				t.Fatal(err)
			}
			wantSyncAbsent(t, srcDir, "dir")
			wantSyncAbsent(t, dstDir, "dir")
		})
	}
}

func TestChecksumSkipsBothSidesBeforeReading(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprint(bisync), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "s")
			putSyncFile(t, dstDir, "file", "target")
			source := &observedSyncFS{FS: src, rejectData: true}
			target := &observedSyncFS{FS: dst, rejectData: true}
			// Only one side matches. Neither side may be opened for hashing.
			skipper := func(info syncer.FileInfo) bool {
				return info.Size() == 1
			}
			var err error
			if bisync {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					Checksum:   true,
					Skipper:    skipper,
					StateStore: &singleSyncStore{},
				})
			} else {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Checksum: true,
					Skipper:  skipper,
				})
			}
			if err != nil || source.dataOpens != 0 || target.dataOpens != 0 {
				t.Fatalf("filtered content was read: reads=%d/%d, err=%v", source.dataOpens, target.dataOpens, err)
			}
			wantSyncFile(t, srcDir, "file", "s")
			wantSyncFile(t, dstDir, "file", "target")
		})
	}
}

func TestChecksumDoesNotReadSkippedTypeConflict(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "dir/file", "source")
			putSyncFile(t, dstDir, "dir", "target")
			source := &observedSyncFS{FS: src, rejectData: true}
			target := &observedSyncFS{FS: dst, rejectData: true}
			var err error
			if mode == "sync" {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Checksum:   true,
					OnConflict: syncer.Skip(),
				})
			} else {
				if mode == "reverse" {
					source, target = target, source
				}
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					Checksum:   true,
					OnConflict: syncer.Skip(),
					StateStore: &singleSyncStore{},
				})
			}
			if err != nil || source.dataOpens != 0 || target.dataOpens != 0 {
				t.Fatalf("skipped conflict read contents: opens=%d/%d, err=%v", source.dataOpens, target.dataOpens, err)
			}
			wantSyncFile(t, srcDir, "dir/file", "source")
			wantSyncFile(t, dstDir, "dir", "target")
		})
	}
}

func TestChecksumDoesNotReadDestinationOnlyFiles(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint(remove), func(t *testing.T) {
			src, dst, _, dstDir := syncPair(t)
			putSyncFile(t, dstDir, "extra", "not compared")
			target := &observedSyncFS{FS: dst, rejectData: true}
			if err := syncer.Sync(context.Background(), src, target, syncer.Option{
				Checksum: true,
				Delete:   remove,
			}); err != nil {
				t.Fatal(err)
			}
			if target.dataOpens != 0 {
				t.Fatal("destination-only file was opened")
			}
			if remove {
				wantSyncAbsent(t, dstDir, "extra")
			} else {
				wantSyncFile(t, dstDir, "extra", "not compared")
			}
		})
	}
}

func TestChecksumStopsReadingAfterConflictAbort(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprint(bisync), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "a", "source")
			putSyncFile(t, dstDir, "a", "target")
			putSyncFile(t, srcDir, "secret", "unvisited")
			source := &excludedIOFS{FS: src, t: t}
			var err error
			if bisync {
				err = syncer.Sync(context.Background(), source, dst, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					Checksum:   true,
					StateStore: &singleSyncStore{},
				})
			} else {
				err = syncer.Sync(context.Background(), source, dst, syncer.Option{Checksum: true})
			}
			if !errors.Is(err, syncer.ErrSyncConflict) {
				t.Fatalf("expected conflict before unvisited file, got %v", err)
			}
			wantSyncFile(t, dstDir, "a", "target")
			wantSyncAbsent(t, dstDir, "secret")
		})
	}
}

func TestChecksumSubtreeComparisonCachesHashes(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprint(restore), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			store := &singleSyncStore{}
			putSyncFile(t, srcDir, "dir/a", "unchanged")
			putSyncFile(t, srcDir, "dir/b", "old")
			if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
				Mode:       syncer.ModeTwoWay,
				Checksum:   true,
				StateStore: store,
			}); err != nil {
				t.Fatal(err)
			}
			putSyncFile(t, dstDir, "dir/b", "new")
			stamp := time.Unix(1000000000, 0)
			for _, path := range []string{"dir/a", "dir/b"} {
				if err := os.Chtimes(filepath.Join(dstDir, path), stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			removeSyncPath(t, srcDir, "dir")
			source, target := &observedSyncFS{FS: src}, &observedSyncFS{FS: dst}
			conflicts := 0
			if err := syncer.Sync(context.Background(), source, target, syncer.Option{
				Mode:       syncer.ModeTwoWay,
				Checksum:   true,
				StateStore: store,
				OnConflict: func(_ context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
					conflicts++
					if conflict.Source != nil || conflict.Target == nil || !conflict.Target.IsDir() {
						t.Fatal("expected deletion versus edited subtree")
					}
					if restore {
						return syncer.ConflictPreferDst, nil
					}
					return syncer.ConflictSkip, nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			reads := 2 // One comparison read per surviving file.
			if restore {
				reads += 2 // Copying reuses planning hashes, then reads transfer streams.
				wantSyncFile(t, srcDir, "dir/a", "unchanged")
				wantSyncFile(t, srcDir, "dir/b", "new")
			} else {
				wantSyncAbsent(t, srcDir, "dir")
			}
			if conflicts != 1 || source.dataOpens != 0 || target.dataOpens != reads {
				t.Fatalf("conflicts=%d opens=%d/%d, want 1 and 0/%d", conflicts, source.dataOpens, target.dataOpens, reads)
			}
			wantSyncFile(t, dstDir, "dir/b", "new")
		})
	}
}

func TestNewFileDryRunDoesNotReadContents(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			putSyncFile(t, srcDir, "file", "new file")
			source := &observedSyncFS{FS: src, rejectData: true}
			target := &observedSyncFS{FS: dst, rejectData: true}
			store := &singleSyncStore{}
			planned := 0
			handler := taskEvents(func(event syncer.Event) {
				switch value := event.(type) {
				case syncer.SyncEvent:
					if value.Status == syncer.StatusPlanned && value.Op == syncer.OpCopy {
						planned++
					}
				}
			})
			var err error
			if mode == "sync" {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					DryRun:     true,
					Checksum:   true,
					StateStore: store,
					OnEvent:    handler,
				})
			} else {
				if mode == "reverse" {
					source, target = target, source
				}
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					DryRun:     true,
					Checksum:   true,
					StateStore: store,
					OnEvent:    handler,
				})
			}
			if err != nil || planned != 1 || source.dataOpens != 0 || target.dataOpens != 0 || store.saves != 0 {
				t.Fatalf("err=%v, planned=%d, reads=%d/%d, saves=%d", err, planned, source.dataOpens, target.dataOpens, store.saves)
			}
		})
	}
}

func TestTypeReplacementDryRunDoesNotReadContents(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprint(bisync), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "dir/file", "source")
			putSyncFile(t, dstDir, "dir", "target")
			source := &observedSyncFS{FS: src, rejectData: true}
			target := &observedSyncFS{FS: dst, rejectData: true}
			var err error
			if bisync {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					DryRun:     true,
					Checksum:   true,
					StateStore: &singleSyncStore{},
					OnConflict: syncer.KeepBoth(),
				})
			} else {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					DryRun:     true,
					Checksum:   true,
					OnConflict: syncer.PreferSrc(),
				})
			}
			if err != nil || source.dataOpens != 0 || target.dataOpens != 0 {
				t.Fatalf("replacement preparation read contents: err=%v, reads=%d/%d", err, source.dataOpens, target.dataOpens)
			}
			wantSyncFile(t, srcDir, "dir/file", "source")
			wantSyncFile(t, dstDir, "dir", "target")
		})
	}
}

// hashSyncFS models a driver with an optional backend hash capability.
type hashSyncFS struct {
	cloudfs.FS
	algorithms []crypto.Hash
	hash       func(context.Context, string, crypto.Hash) (string, error)
}

func (fsys *hashSyncFS) Hashes() []crypto.Hash {
	return fsys.algorithms
}

func (fsys *hashSyncFS) Hash(ctx context.Context, path string, algorithm crypto.Hash) (string, error) {
	return fsys.hash(ctx, path, algorithm)
}

func TestChecksumUsesHasher(t *testing.T) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("data")))
	failure := errors.New("backend hash failed")
	for _, test := range []struct {
		name    string
		hash    string
		err     error
		wantErr error
		reads   int
	}{
		{name: "backend", hash: digest},
		{name: "empty", reads: 1},
		{name: "unsupported", err: fmt.Errorf("hash: %w", cloudfs.ErrNotSupport), reads: 1},
		{name: "error", err: failure, wantErr: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "folder/file", "data")
			putSyncFile(t, dstDir, "file", "data")
			source := &observedSyncFS{FS: src}
			calls := 0
			backend := &hashSyncFS{
				FS: source,
				hash: func(ctx context.Context, path string, algorithm crypto.Hash) (string, error) {
					calls++
					if path != "/folder/file" || algorithm != crypto.SHA256 {
						t.Fatalf("unexpected hash request: %s %v", path, algorithm)
					}
					return test.hash, test.err
				},
			}
			prefixed, err := cloudfs.New(backend, middleware.PrefixFS("/folder"))
			if err != nil {
				t.Fatal(err)
			}
			err = syncer.Sync(context.Background(), prefixed, dst, syncer.Option{
				Checksum: true,
				DryRun:   true,
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("sync error = %v, want %v", err, test.wantErr)
			}
			if calls != 1 || source.dataOpens != test.reads {
				t.Fatalf("hash calls = %d, file reads = %d; want 1, %d", calls, source.dataOpens, test.reads)
			}
		})
	}
}

func TestSyncSelectsHashAlgorithm(t *testing.T) {
	for _, mode := range []syncer.Mode{syncer.ModeOneWay, syncer.ModeTwoWay} {
		for _, test := range []struct {
			name   string
			source []crypto.Hash
			target []crypto.Hash
			want   crypto.Hash
		}{
			{name: "md5", source: []crypto.Hash{crypto.MD5}, target: []crypto.Hash{crypto.MD5}, want: crypto.MD5},
			{name: "sha1", source: []crypto.Hash{crypto.SHA1}, target: []crypto.Hash{crypto.SHA1}, want: crypto.SHA1},
			{name: "source preference", source: []crypto.Hash{crypto.MD5, crypto.SHA256}, target: []crypto.Hash{crypto.SHA256, crypto.MD5}, want: crypto.MD5},
			{name: "intersection", source: []crypto.Hash{crypto.MD5, crypto.SHA1}, target: []crypto.Hash{crypto.SHA1}, want: crypto.SHA1},
			{name: "no intersection", source: []crypto.Hash{crypto.MD5}, target: []crypto.Hash{crypto.SHA1}, want: crypto.SHA256},
			{name: "empty", want: crypto.SHA256},
			{name: "unavailable", source: []crypto.Hash{crypto.Hash(255), crypto.MD5}, target: []crypto.Hash{crypto.Hash(255), crypto.MD5}, want: crypto.MD5},
		} {
			t.Run(fmt.Sprintf("mode=%d/%s", mode, test.name), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				putSyncFile(t, srcDir, "file", "data")
				putSyncFile(t, dstDir, "file", "data")
				putSyncFile(t, srcDir, "new", "new content")
				source, target := &observedSyncFS{FS: src}, &observedSyncFS{FS: dst}
				left := &hashSyncFS{FS: source, algorithms: test.source}
				right := &hashSyncFS{FS: target, algorithms: test.target}
				for _, backend := range []*hashSyncFS{left, right} {
					backend.hash = func(ctx context.Context, path string, algorithm crypto.Hash) (string, error) {
						if algorithm != test.want || path != "/file" {
							t.Fatalf("unexpected hash request: %s %v", path, algorithm)
						}
						if !slices.Contains(backend.algorithms, algorithm) {
							return "", cloudfs.ErrNotSupport
						}
						hash := algorithm.New()
						hash.Write([]byte("data"))
						return fmt.Sprintf("%x", hash.Sum(nil)), nil
					}
				}
				sourceFS, err := cloudfs.New(left, middleware.PrefixFS("/"))
				if err != nil {
					t.Fatal(err)
				}
				targetFS, err := cloudfs.New(right, middleware.PrefixFS("/"))
				if err != nil {
					t.Fatal(err)
				}
				store := &singleSyncStore{}
				option := syncer.Option{Mode: mode, Checksum: true, StateStore: store}
				if err := syncer.Sync(context.Background(), sourceFS, targetFS, option); err != nil {
					t.Fatal(err)
				}
				state, err := store.Load(context.Background())
				if err != nil || !state.Valid() || state.HashAlgorithm != test.want {
					t.Fatalf("invalid state: %+v, %v", state, err)
				}
				invalid := *state
				invalid.HashAlgorithm = crypto.SHA512
				if invalid.Valid() {
					t.Fatal("state accepted digests with the wrong algorithm length")
				}
				for path, content := range map[string]string{"file": "data", "new": "new content"} {
					hash := test.want.New()
					hash.Write([]byte(content))
					want := fmt.Sprintf("%x", hash.Sum(nil))
					if state.Source[path].Hash != want || state.Target[path].Hash != want {
						t.Fatalf("%s has the wrong comparison or transfer hash", path)
					}
				}
				if slices.Contains(test.source, test.want) && slices.Contains(test.target, test.want) &&
					(source.dataOpens != 1 || target.dataOpens != 0) {
					t.Fatalf("backend comparison downloaded files: reads=%d/%d", source.dataOpens, target.dataOpens)
				}
				// Losing backend support must preserve the algorithm of the baseline.
				left.algorithms, right.algorithms = nil, nil
				left.hash = func(ctx context.Context, path string, algorithm crypto.Hash) (string, error) {
					if algorithm != test.want {
						t.Fatalf("baseline algorithm changed to %v", algorithm)
					}
					return "", cloudfs.ErrNotSupport
				}
				right.hash = left.hash
				saves := store.saves
				if err := syncer.Sync(context.Background(), sourceFS, targetFS, option); err != nil {
					t.Fatal(err)
				}
				if store.saves != saves {
					t.Fatal("unchanged files rewrote state after backend support changed")
				}
			})
		}
	}
}
