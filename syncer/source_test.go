package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

func TestSourceChangesAreNotConfirmed(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		for _, phase := range []string{"before", "during"} {
			for _, change := range []string{"size", "time", "directory", "removed"} {
				t.Run(mode+"/"+phase+"/"+change, func(t *testing.T) {
					src, dst, srcDir, dstDir := syncPair(t)
					putSyncFile(t, srcDir, "file", "source-data")
					putSyncFile(t, dstDir, "file", "previous-target")
					store := &singleSyncStore{}
					changed, failed, succeeded, handled := false, false, false, false
					handler := taskEvents(func(notification syncer.Event) {
						status := syncer.Status(255)
						var eventErr error
						switch event := notification.(type) {
						case syncer.SyncEvent:
							status, eventErr = event.Status, event.Err
						case syncer.ProgressEvent:
							if phase == "during" && event.Written > 0 && !changed {
								changeSyncSource(t, srcDir, change)
								changed = true
							}
						}
						if phase == "before" && status == syncer.StatusStarted && !changed {
							changeSyncSource(t, srcDir, change)
							changed = true
						}
						if status == syncer.StatusSucceeded {
							succeeded = true
						}
						if status == syncer.StatusFailed {
							failed = errors.Is(eventErr, syncer.ErrSourceChanged)
						}
					})
					onError := func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
						if !errors.Is(failure.Err, syncer.ErrSourceChanged) {
							t.Fatalf("unexpected error: %v", failure.Err)
						}
						if !handled {
							handled = true
							return syncer.ErrorRetry, nil
						}
						return syncer.ErrorAbort, nil
					}
					var err error
					if mode == "sync" {
						err = syncer.Sync(context.Background(), src, dst, syncer.Option{

							StateStore: store,
							OnConflict: syncer.PreferSrc(),
							OnEvent:    handler,
							OnError:    onError,
						})
					} else {
						left, right, policy := src, dst, syncer.ConflictPreferSrc
						if mode == "reverse" {
							left, right, policy = dst, src, syncer.ConflictPreferDst
						}
						err = syncer.Sync(context.Background(), left, right, syncer.Option{
							Mode:       syncer.ModeTwoWay,
							StateStore: store,
							OnConflict: fixedConflict(policy),
							OnEvent:    handler,
							OnError:    onError,
						})
					}
					if !errors.Is(err, syncer.ErrSourceChanged) || !changed || !handled || !failed || succeeded || store.saves != 0 {
						t.Fatalf("err=%v changed=%t handled=%t failed=%t succeeded=%t saves=%d", err, changed, handled, failed, succeeded, store.saves)
					}
					if phase == "before" {
						wantSyncFile(t, dstDir, "file", "previous-target")
					}
				})
			}
		}
	}
}

func changeSyncSource(t *testing.T, root, change string) {
	t.Helper()
	switch change {
	case "size":
		putSyncFile(t, root, "file", "changed source with different size")
	case "time":
		// Keep size unchanged and change only the timestamp deterministically.
		stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(root, "file"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	case "directory":
		removeSyncPath(t, root, "file")
		if err := os.Mkdir(filepath.Join(root, "file"), 0700); err != nil {
			t.Fatal(err)
		}
	case "removed":
		removeSyncPath(t, root, "file")
	}
}

func TestBiSyncDeletionConflictChecksTransferHash(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "old!")
			store := &singleSyncStore{}
			option := syncer.Option{
				Mode:       syncer.ModeTwoWay,
				Checksum:   true,
				StateStore: store,
				OnConflict: syncer.PreferSrc(),
			}
			ctx := context.Background()
			if err := syncer.Sync(ctx, src, dst, option); err != nil {
				t.Fatal(err)
			}
			baseline, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			survivor, removed := srcDir, dstDir
			if reverse {
				survivor, removed = dstDir, srcDir
				option.OnConflict = syncer.PreferDst()
			}
			removeSyncPath(t, removed, "file")
			putSyncFile(t, survivor, "file", "new!")
			option.OnEvent = taskEvents(func(notification syncer.Event) {
				event, ok := notification.(syncer.SyncEvent)
				if !ok || event.Status != syncer.StatusStarted || event.Op != syncer.OpCopy {
					return
				}
				filename := filepath.Join(survivor, "file")
				info, err := os.Stat(filename)
				if err != nil {
					t.Fatal(err)
				}
				putSyncFile(t, survivor, "file", "race")
				if err := os.Chtimes(filename, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			})
			if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, syncer.ErrSourceChanged) {
				t.Fatalf("changed transfer was confirmed: %v", err)
			}
			state, err := store.Load(ctx)
			if err != nil || !reflect.DeepEqual(state.Source, baseline.Source) || !reflect.DeepEqual(state.Target, baseline.Target) {
				t.Fatalf("failed transfer advanced the baseline: %+v, %v", state, err)
			}
		})
	}
}

func TestChecksumComparisonRejectsSourceChanges(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprintf("bisync=%t", bisync), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "file", "source-data")
			putSyncFile(t, dstDir, "file", "target-data")
			source := &sourceCloseHookFS{
				FS: src,
				afterClose: func() {
					changeSyncSource(t, srcDir, "time")
				},
			}
			target := &previewReadOnlyFS{FS: dst}
			store := &singleSyncStore{}
			onError := func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
				t.Fatal("comparison failure entered OnError")
				return syncer.ErrorAbort, failure.Err
			}
			var err error
			if bisync {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					Checksum:   true,
					StateStore: store,
					OnError:    onError,
				})
			} else {
				err = syncer.Sync(context.Background(), source, target, syncer.Option{
					Checksum: true,

					StateStore: store,
					OnError:    onError,
				})
			}
			if !errors.Is(err, syncer.ErrSourceChanged) || target.writes != 0 || store.saves != 0 {
				t.Fatalf("err=%v writes=%d saves=%d", err, target.writes, store.saves)
			}
		})
	}
}

func TestSourceChangePreservesCloseError(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "source-data")
	source := &sourceCloseHookFS{
		FS:       src,
		closeErr: errSyncTest,
		afterClose: func() {
			changeSyncSource(t, srcDir, "time")
		},
	}
	store := &singleSyncStore{}
	err := syncer.Sync(context.Background(), source, dst, syncer.Option{

		StateStore: store,
	})
	if !errors.Is(err, syncer.ErrSourceChanged) || !errors.Is(err, errSyncTest) || store.saves != 0 {
		t.Fatalf("err=%v saves=%d", err, store.saves)
	}
}

func TestSourceCheckPreservesDriverError(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			putSyncFile(t, srcDir, "file", "source-data")
			source := &sourceStatFailureFS{
				FS:     src,
				failAt: failAt,
			}
			called := false
			err := syncer.Sync(context.Background(), source, dst, syncer.Option{
				OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
					called = true
					if !errors.Is(failure.Err, errSyncTest) || failure.Err.Error() != errSyncTest.Error() {
						t.Fatalf("source check changed driver error: %v", failure.Err)
					}
					return syncer.ErrorAbort, failure.Err
				},
			})
			if !called || !errors.Is(err, errSyncTest) || err.Error() != errSyncTest.Error() {
				t.Fatalf("called=%t err=%v", called, err)
			}
		})
	}
}

type sourceStatFailureFS struct {
	cloudfs.FS
	calls  int
	failAt int
}

func (fsys *sourceStatFailureFS) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	if path == "/file" {
		fsys.calls++
		if fsys.calls == fsys.failAt {
			return nil, errSyncTest
		}
	}
	return fsys.FS.Stat(ctx, path)
}

type sourceCloseHookFS struct {
	cloudfs.FS
	afterClose func()
	closeErr   error
}

func (fsys *sourceCloseHookFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	file, err := fsys.FS.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return &sourceCloseHookFile{
		File:       file,
		afterClose: fsys.afterClose,
		closeErr:   fsys.closeErr,
	}, nil
}

type sourceCloseHookFile struct {
	cloudfs.File
	afterClose func()
	closeErr   error
}

func (file *sourceCloseHookFile) Close() error {
	err := file.File.Close()
	file.afterClose()
	return errors.Join(err, file.closeErr)
}
