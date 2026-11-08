package syncer_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

func TestDryRunMatchesExecution(t *testing.T) {
	for _, bidirectional := range []bool{false, true} {
		t.Run(fmt.Sprintf("bisync=%t", bidirectional), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			store := &singleSyncStore{}
			ctx := context.Background()
			run := func(left, right cloudfs.FS, dryRun bool, handler syncer.EventHandler) error {
				if bidirectional {
					return syncer.Sync(ctx, left, right, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						DryRun:     dryRun,
						StateStore: store,
						OnConflict: syncer.PreferSrc(),
						OnEvent:    handler,
					})
				}
				return syncer.Sync(ctx, left, right, syncer.Option{
					DryRun: dryRun,
					Delete: true,

					StateStore: store,
					OnConflict: syncer.PreferSrc(),
					OnEvent:    handler,
				})
			}
			putSyncFile(t, srcDir, "removed/child", "old")
			putSyncFile(t, srcDir, "replaced", "old file")
			if err := run(src, dst, false, nil); err != nil {
				t.Fatal(err)
			}
			removeSyncPath(t, srcDir, "removed")
			removeSyncPath(t, srcDir, "replaced")
			putSyncFile(t, srcDir, "replaced/child", "new directory")
			putSyncFile(t, dstDir, "other", "other side")
			before := []map[string]string{biSyncSubtree(t, srcDir, "."), biSyncSubtree(t, dstDir, ".")}
			state, saves := bytes.Clone(store.data), store.saves
			var planned, executed []taskEventSnapshot
			handler := taskEvents(func(event syncer.Event) {
				var snapshot taskEventSnapshot
				switch event := event.(type) {
				case syncer.TaskEvent:
					snapshot = snapshotTaskEvent(event)
				default:
					t.Fatalf("unexpected preview event: %T", event)
				}
				if snapshot.Status != syncer.StatusPlanned || snapshot.Err != nil {
					t.Fatalf("unexpected preview status: %+v", snapshot)
				}
				if snapshot.Status.String() != "planned" {
					t.Fatalf("preview status string: %s", snapshot.Status)
				}
				snapshot.Status = syncer.StatusStarted
				planned = append(planned, snapshot)
			})
			left, right := &previewReadOnlyFS{FS: src}, &previewReadOnlyFS{FS: dst}
			if err := run(left, right, true, handler); err != nil {
				t.Fatal(err)
			}
			if left.writes+right.writes != 0 || store.saves != saves || !bytes.Equal(store.data, state) {
				t.Fatal("preview attempted writes or changed state")
			}
			if got := []map[string]string{biSyncSubtree(t, srcDir, "."), biSyncSubtree(t, dstDir, ".")}; !reflect.DeepEqual(got, before) {
				t.Fatal("preview changed file contents")
			}
			if err := run(src, dst, false, taskEvents(func(event syncer.Event) {
				var snapshot taskEventSnapshot
				switch event := event.(type) {
				case syncer.TaskEvent:
					snapshot = snapshotTaskEvent(event)
				default:
					return
				}
				if snapshot.Status == syncer.StatusStarted {
					executed = append(executed, snapshot)
				}
			})); err != nil {
				t.Fatal(err)
			}
			if len(planned) == 0 || !reflect.DeepEqual(planned, executed) {
				t.Fatalf("planned %v, executed %v", planned, executed)
			}
		})
	}
}

func TestDeleteLimits(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		for _, replacement := range []bool{false, true} {
			for _, limit := range []struct {
				maxDelete int
				percent   int
				blocked   bool
			}{
				{
					maxDelete: 2,
					blocked:   true,
				},
				{
					percent: 74,
					blocked: true,
				},
				{
					maxDelete: 100,
					percent:   74,
					blocked:   true,
				},
				{
					maxDelete: 3,
					percent:   75,
				},
			} {
				t.Run(fmt.Sprintf("%s/replacement=%t/max=%d/percent=%d", mode, replacement, limit.maxDelete, limit.percent), func(t *testing.T) {
					src, dst, srcDir, dstDir := syncPair(t)
					store := &singleSyncStore{}
					ctx := context.Background()
					run := func(left, right cloudfs.FS, maxDelete, percent int) error {
						if mode == "sync" {
							return syncer.Sync(ctx, left, right, syncer.Option{
								Delete: !replacement,

								Checksum:         true,
								StateStore:       store,
								OnConflict:       syncer.PreferSrc(),
								MaxDelete:        maxDelete,
								MaxDeletePercent: percent,
							})
						}
						policy := syncer.ConflictPreferSrc
						if mode == "reverse" {
							policy = syncer.ConflictPreferDst
						}
						return syncer.Sync(ctx, left, right, syncer.Option{
							Mode:             syncer.ModeTwoWay,
							Checksum:         true,
							StateStore:       store,
							OnConflict:       fixedConflict(policy),
							MaxDelete:        maxDelete,
							MaxDeletePercent: percent,
						})
					}
					putSyncFile(t, srcDir, "tree/a", "a")
					putSyncFile(t, srcDir, "tree/b", "b")
					putSyncFile(t, srcDir, "keep", "keep")
					if err := run(src, dst, 0, 0); err != nil {
						t.Fatal(err)
					}
					changed := srcDir
					if mode == "reverse" {
						changed = dstDir
					}
					removeSyncPath(t, changed, "tree")
					if replacement {
						putSyncFile(t, changed, "tree", "new file")
					}
					putSyncFile(t, changed, "a-first", "must wait for guard")
					if limit.blocked {
						state, saves := bytes.Clone(store.data), store.saves
						left, right := &previewReadOnlyFS{FS: src}, &previewReadOnlyFS{FS: dst}
						if err := run(left, right, limit.maxDelete, limit.percent); !errors.Is(err, syncer.ErrTooManyDeletes) {
							t.Fatalf("got %v", err)
						}
						if left.writes+right.writes != 0 || store.saves != saves || !bytes.Equal(state, store.data) {
							t.Fatal("blocked plan attempted writes or changed state")
						}
					} else if err := run(src, dst, limit.maxDelete, limit.percent); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestBiSyncDeleteLimitCountsBothSides(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "left/child", "left")
	putSyncFile(t, dstDir, "right/child", "right")
	option := syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{},
	}
	ctx := context.Background()
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	removeSyncPath(t, srcDir, "left")
	removeSyncPath(t, dstDir, "right")
	option.MaxDelete = 3
	option.DryRun = true
	if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, syncer.ErrTooManyDeletes) {
		t.Fatalf("combined limit: %v", err)
	}
	option.MaxDelete = 4
	option.MaxDeletePercent = 99
	if err := syncer.Sync(ctx, src, dst, option); !errors.Is(err, syncer.ErrTooManyDeletes) {
		t.Fatalf("per-side percentage: %v", err)
	}
	option.MaxDeletePercent = 100
	option.DryRun = false
	if err := syncer.Sync(ctx, src, dst, option); err != nil {
		t.Fatal(err)
	}
	wantSyncAbsent(t, srcDir, "right")
	wantSyncAbsent(t, dstDir, "left")
}

func TestDryRunCancellationAndConflict(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprintf("bisync=%t", bisync), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "a", "source")
			putSyncFile(t, srcDir, "b", "source")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			handler := taskEvents(func(syncer.Event) {
				calls++
				cancel()
			})
			store := &singleSyncStore{}
			run := func(ctx context.Context, handler syncer.EventHandler) error {
				if bisync {
					return syncer.Sync(ctx, src, dst, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						DryRun:     true,
						StateStore: store,
						OnEvent:    handler,
					})
				}
				return syncer.Sync(ctx, src, dst, syncer.Option{
					DryRun: true,

					StateStore: store,
					OnEvent:    handler,
				})
			}
			if err := run(ctx, handler); !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("cancellation: calls=%d err=%v", calls, err)
			}
			putSyncFile(t, dstDir, "b", "conflicting file")
			if err := run(context.Background(), handler); !errors.Is(err, syncer.ErrSyncConflict) || calls != 1 {
				t.Fatalf("conflict: calls=%d err=%v", calls, err)
			}
			if store.saves != 0 {
				t.Fatal("dry run saved first-run state")
			}
		})
	}
}

func TestInvalidDeleteLimits(t *testing.T) {
	for _, limits := range [][2]int{{-1, 0}, {0, -1}, {0, 101}} {
		if err := syncer.Sync(context.Background(), nil, nil, syncer.Option{
			MaxDelete:        limits[0],
			MaxDeletePercent: limits[1],
		}); err == nil {
			t.Fatalf("Sync accepted %v", limits)
		}
		if err := syncer.Sync(context.Background(), nil, nil, syncer.Option{
			Mode:             syncer.ModeTwoWay,
			MaxDelete:        limits[0],
			MaxDeletePercent: limits[1],
		}); err == nil {
			t.Fatalf("BiSync accepted %v", limits)
		}
	}
}

func TestDeleteLimitIgnoresFilteredRemovals(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprintf("bisync=%t", bisync), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, dstDir, "excluded/a", "a")
			putSyncFile(t, dstDir, "excluded/b", "b")
			putSyncFile(t, srcDir, "new", "new")
			left, right := &previewReadOnlyFS{FS: src}, &previewReadOnlyFS{FS: dst}
			skipper := func(info syncer.FileInfo) bool {
				return info.Path() == "excluded"
			}
			var err error
			if bisync {
				err = syncer.Sync(context.Background(), left, right, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					DryRun:     true,
					StateStore: &singleSyncStore{},
					MaxDelete:  1,
					Skipper:    skipper,
				})
			} else {
				err = syncer.Sync(context.Background(), left, right, syncer.Option{
					DryRun:    true,
					Delete:    true,
					MaxDelete: 1,
					Skipper:   skipper,
				})
			}
			if err != nil || left.writes+right.writes != 0 {
				t.Fatalf("filtered preview: writes=%d err=%v", left.writes+right.writes, err)
			}
		})
	}
}

type previewReadOnlyFS struct {
	cloudfs.FS
	writes int
}

func (fsys *previewReadOnlyFS) Create(context.Context, string) (cloudfs.FileWriter, error) {
	fsys.writes++
	return nil, errSyncTest
}

func (fsys *previewReadOnlyFS) MakeDir(context.Context, string) error {
	fsys.writes++
	return errSyncTest
}

func (fsys *previewReadOnlyFS) Remove(context.Context, string) error {
	fsys.writes++
	return errSyncTest
}
