package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/syncer"
)

func TestCancellationSavesConfirmedResults(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		for _, stop := range []string{"/a", "/b"} {
			for _, failSave := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stop=%s/save-fails=%t", mode, stop, failSave), func(t *testing.T) {
					src, dst, srcDir, dstDir := syncPair(t)
					putSyncFile(t, srcDir, "a", "first")
					putSyncFile(t, srcDir, "b", "second")
					store := &checkpointStore{}
					store.failSave = failSave
					ctx, cancel := context.WithCancel(context.WithValue(context.Background(), checkpointKey{}, "retained"))
					defer cancel()
					run := func(ctx context.Context, handler syncer.EventHandler) error {
						if mode == "sync" {
							return syncer.Sync(ctx, src, dst, syncer.Option{

								StateStore: store,
								OnEvent:    handler,
							})
						}
						left, right := src, dst
						if mode == "reverse" {
							left, right = dst, src
						}
						return syncer.Sync(ctx, left, right, syncer.Option{
							Mode:       syncer.ModeTwoWay,
							StateStore: store,
							OnEvent:    handler,
						})
					}
					err := run(ctx, taskEvents(func(notification syncer.Event) {
						var event taskEventSnapshot
						switch value := notification.(type) {
						case syncer.SyncEvent:
							event = snapshotTaskEvent(value)
						default:
							return
						}
						if event.Status == syncer.StatusSucceeded && event.Path == stop {
							cancel()
						}
					}))
					if !errors.Is(err, context.Canceled) || errors.Is(err, errSyncTest) != failSave {
						t.Fatalf("got %v", err)
					}
					if store.saves != 1 || store.contextErr != nil || store.value != "retained" || !store.bounded {
						t.Fatalf("saves=%d context-error=%v value=%v bounded=%t", store.saves, store.contextErr, store.value, store.bounded)
					}
					if failSave {
						if len(store.data) != 0 {
							t.Fatal("failed save published state")
						}
						return
					}
					state, err := store.Load(context.Background())
					if err != nil || state == nil || !state.Valid() || state.Source["a"] == nil || state.Target["a"] == nil {
						t.Fatalf("invalid checkpoint: %+v, %v", state, err)
					}
					if stop == "/a" {
						wantSyncAbsent(t, dstDir, "b")
						if state.Source["b"] != nil || state.Target["b"] != nil {
							t.Fatal("unexecuted file was confirmed")
						}
					} else if state.Source["b"] == nil || state.Target["b"] == nil {
						t.Fatal("last successful file was not saved")
					}
					// Resume with the default conflict policy. Confirmed files must not
					// be recopied or mistaken for conflicting edits.
					if err := run(context.Background(), taskEvents(func(notification syncer.Event) {
						var event taskEventSnapshot
						switch value := notification.(type) {
						case syncer.SyncEvent:
							event = snapshotTaskEvent(value)
						default:
							return
						}
						if event.Path == "/a" || stop == "/b" {
							t.Fatalf("confirmed file executed again: %+v", event)
						}
					})); err != nil {
						t.Fatal(err)
					}
					wantSyncFile(t, dstDir, "a", "first")
					wantSyncFile(t, dstDir, "b", "second")
				})
			}
		}
	}
}

func TestCancellationSavesPartialReplacement(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, stop := range []syncer.Op{syncer.OpMkdir, syncer.OpRemove, syncer.OpRmdir} {
			t.Run(fmt.Sprintf("reverse=%t/stop=%s", reverse, stop), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				store := &checkpointStore{}
				option := syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: store,
				}
				if stop == syncer.OpRemove {
					putSyncFile(t, srcDir, "path", "old file")
				} else if stop == syncer.OpRmdir {
					putSyncFile(t, srcDir, "path/old", "old child")
				}
				if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
					t.Fatal(err)
				}
				changed := srcDir
				option.OnConflict = syncer.PreferSrc()
				if reverse {
					changed = dstDir
					option.OnConflict = syncer.PreferDst()
				}
				removeSyncPath(t, changed, "path")
				if stop == syncer.OpRmdir {
					putSyncFile(t, changed, "path", "new file")
				} else {
					putSyncFile(t, changed, "path/new", "new child")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				option.OnEvent = taskEvents(func(notification syncer.Event) {
					if event, ok := notification.(syncer.SyncEvent); ok && event.Op == stop && event.Status == syncer.StatusSucceeded {
						cancel()
					}
				})
				if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
				state, err := store.Load(context.Background())
				if err != nil || state == nil || !state.Valid() {
					t.Fatalf("invalid replacement checkpoint: %+v, %v", state, err)
				}
				// The next run must reconstruct the remaining work from the saved
				// per-side results without another conflict preference.
				option.OnConflict = syncer.Abort()
				option.OnEvent = nil
				if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
					t.Fatal(err)
				}
				for _, root := range []string{srcDir, dstDir} {
					if stop == syncer.OpRmdir {
						wantSyncFile(t, root, "path", "new file")
					} else {
						wantSyncFile(t, root, "path/new", "new child")
					}
				}
			})
		}
	}
}

func TestCancellationDoesNotConfirmActiveCopy(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "a", "first")
	putSyncFile(t, srcDir, "b", "unconfirmed second file")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &checkpointStore{}
	err := syncer.Sync(ctx, src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store,
		OnEvent: taskEvents(func(notification syncer.Event) {
			if event, ok := notification.(syncer.ProgressEvent); ok && event.FileInfo.Path() == "/b" && event.Written > 0 {
				cancel()
			}
		}),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	state, err := store.Load(context.Background())
	if err != nil || state == nil || !state.Valid() || state.Source["a"] == nil || state.Target["a"] == nil {
		t.Fatalf("invalid checkpoint: %+v, %v", state, err)
	}
	if state.Source["b"] != nil || state.Target["b"] != nil {
		t.Fatal("active copy was confirmed after cancellation")
	}
}

func TestCancellationSavesLogicalDeletion(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "removed/child", "old child")
			store := &checkpointStore{}
			option := syncer.Option{
				Mode:       syncer.ModeTwoWay,
				StateStore: store,
			}
			if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
				t.Fatal(err)
			}
			changed := srcDir
			if reverse {
				changed = dstDir
			}
			removeSyncPath(t, changed, "removed")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			option.OnEvent = taskEvents(func(notification syncer.Event) {
				if event, ok := notification.(syncer.SyncEvent); ok && event.Status == syncer.StatusSucceeded {
					cancel()
				}
			})
			if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
			state, err := store.Load(context.Background())
			if err != nil || state == nil || !state.Valid() || len(state.Source)+len(state.Target) != 0 {
				t.Fatalf("deletion was not confirmed on both sides: %+v, %v", state, err)
			}
			option.OnEvent = taskEvents(func(event syncer.Event) {
				t.Fatalf("deletion executed again: %+v", event)
			})
			saves := store.saves
			if err := syncer.Sync(context.Background(), src, dst, option); err != nil || store.saves != saves {
				t.Fatalf("idle run: %v, saves=%d", err, store.saves)
			}
		})
	}
}

func TestStateSaveKeepsActiveContext(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "data")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &checkpointStore{}
	if err := syncer.Sync(ctx, src, dst, syncer.Option{

		StateStore: store,
	}); err != nil {
		t.Fatal(err)
	}
	if store.savedContext != ctx || store.bounded {
		t.Fatal("normal save replaced the caller context")
	}
}

type checkpointKey struct{}

type checkpointStore struct {
	singleSyncStore
	contextErr   error
	value        any
	bounded      bool
	savedContext context.Context
}

func (store *checkpointStore) Save(ctx context.Context, state *syncer.State) error {
	store.savedContext = ctx
	store.contextErr = ctx.Err()
	store.value = ctx.Value(checkpointKey{})
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline)
	store.bounded = ok && remaining > 0 && remaining <= 5*time.Second
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.singleSyncStore.Save(ctx, state)
}
