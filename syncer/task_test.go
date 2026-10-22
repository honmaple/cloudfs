package syncer_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/honmaple/cloudfs/syncer"
)

// taskEvents keeps task-focused tests independent of lifecycle notifications.
func taskEvents(handler syncer.EventHandler) syncer.EventHandler {
	return func(event syncer.Event) {
		if _, ok := event.(syncer.PhaseEvent); ok {
			return
		}
		handler(event)
	}
}

type taskEventSnapshot struct {
	Path    string
	Op      syncer.Op
	Reverse bool
	NewPath string
	Status  syncer.Status
	Err     error
}

func snapshotTaskEvent(event syncer.SyncEvent) taskEventSnapshot {
	return taskEventSnapshot{
		Path:    event.FileInfo.Path(),
		Op:      event.Op,
		Reverse: event.Reverse,
		NewPath: event.NewPath,
		Status:  event.Status,
		Err:     event.Err,
	}
}

func TestSharedEventHandler(t *testing.T) {
	var syncEvents, biSyncEvents int
	handler := syncer.EventHandler(taskEvents(func(event syncer.Event) {
		if _, ok := event.(syncer.ProgressEvent); ok {
			return
		}

		task, ok := event.(syncer.SyncEvent)
		if !ok || task.FileInfo.Path() != "/file" || task.Op != syncer.OpCopy {
			t.Fatalf("unexpected task event: %+v", task)
		}
		if task.Reverse {
			biSyncEvents++
		} else {
			syncEvents++
		}
	}))
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	if err := syncer.Sync(context.Background(), src, dst, syncer.Option{OnEvent: handler}); err != nil {
		t.Fatal(err)
	}
	src, dst, _, dstDir := syncPair(t)
	putSyncFile(t, dstDir, "file", "contents")
	if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{}, OnEvent: handler,
	}); err != nil {
		t.Fatal(err)
	}
	if syncEvents != 2 || biSyncEvents != 2 {
		t.Fatalf("sync events=%d, bisync events=%d", syncEvents, biSyncEvents)
	}
}

func TestSyncTaskEvents(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "dir/file", "contents")
	putSyncFile(t, dstDir, "old/file", "remove")
	var retained []syncer.Event
	if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		Delete: true,
		OnEvent: taskEvents(func(event syncer.Event) {
			if _, ok := event.(syncer.ProgressEvent); ok {
				return
			}
			retained = append(retained, event)
		}),
	}); err != nil {
		t.Fatal(err)
	}
	var events []taskEventSnapshot
	for _, event := range retained {
		events = append(events, snapshotTaskEvent(event.(syncer.SyncEvent)))
	}
	want := []taskEventSnapshot{
		{Path: "/dir", Op: syncer.OpMkdir, Status: syncer.StatusStarted},
		{Path: "/dir", Op: syncer.OpMkdir, Status: syncer.StatusSucceeded},
		{Path: "/dir/file", Op: syncer.OpCopy, Status: syncer.StatusStarted},
		{Path: "/dir/file", Op: syncer.OpCopy, Status: syncer.StatusSucceeded},
		{Path: "/old", Op: syncer.OpRmdir, Status: syncer.StatusStarted},
		{Path: "/old", Op: syncer.OpRmdir, Status: syncer.StatusSucceeded},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%+v, want %+v", events, want)
	}
}

func TestSyncTaskEventsAfterErrorHandling(t *testing.T) {
	for _, mode := range []string{"retry", "skip", "stop", "cancel", "cancel-abort", "stat"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			putSyncFile(t, srcDir, "file", "contents")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var events []taskEventSnapshot
			option := syncer.Option{OnEvent: taskEvents(func(event syncer.Event) {
				if _, ok := event.(syncer.ProgressEvent); ok {
					return
				}
				events = append(events, snapshotTaskEvent(event.(syncer.SyncEvent)))
			})}
			if mode == "stat" {
				dst = &targetStatFault{FS: dst, failures: 1}
			} else {
				src = &sourceReadFault{FS: src, remainingFailures: 1}
			}
			option.OnError = func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
				switch mode {
				case "retry":
					return syncer.ErrorRetry, nil
				case "skip":
					return syncer.ErrorSkip, nil
				case "cancel":
					cancel()
					return syncer.ErrorSkip, nil
				case "cancel-abort":
					cancel()
					return syncer.ErrorAbort, nil
				case "stat":
					t.Fatal("metadata query called OnError")
				}
				return syncer.ErrorAbort, failure.Err
			}
			err := syncer.Sync(ctx, src, dst, option)
			if len(events) != 2 || events[0].Status != syncer.StatusStarted || events[1].Path != "/file" || events[1].Op != syncer.OpCopy {
				t.Fatalf("events=%+v", events)
			}
			status := syncer.StatusFailed
			switch mode {
			case "retry":
				status = syncer.StatusSucceeded
			case "skip", "cancel":
				status = syncer.StatusSkipped
			}
			if events[1].Status != status {
				t.Fatalf("status=%v, want %v", events[1].Status, status)
			}
			if status == syncer.StatusFailed {
				wantErr := errSyncTest
				if mode == "cancel-abort" && !errors.Is(err, context.Canceled) {
					t.Fatalf("missing cancellation: %v", err)
				}
				if !errors.Is(err, wantErr) || !errors.Is(events[1].Err, wantErr) {
					t.Fatalf("result=%v event=%v", err, events[1].Err)
				}
			} else if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || events[1].Err != nil {
					t.Fatalf("result=%v event=%v", err, events[1].Err)
				}
			} else if err != nil || events[1].Err != nil {
				t.Fatalf("result=%v event=%v", err, events[1].Err)
			}
		})
	}
}

func TestSyncTaskEventsForBlockedChildren(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "dir/file", "contents")
	var events []taskEventSnapshot
	err := syncer.Sync(context.Background(), src, &fileOperationFaultFS{FS: dst}, syncer.Option{
		OnEvent: taskEvents(func(event syncer.Event) {
			if _, ok := event.(syncer.ProgressEvent); ok {
				return
			}
			events = append(events, snapshotTaskEvent(event.(syncer.SyncEvent)))
		}),
		OnError: func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
			return syncer.ErrorSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []taskEventSnapshot{
		{Path: "/dir", Op: syncer.OpMkdir, Status: syncer.StatusStarted},
		{Path: "/dir", Op: syncer.OpMkdir, Status: syncer.StatusSkipped},
		{Path: "/dir/file", Op: syncer.OpCopy, Status: syncer.StatusSkipped},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%+v, want %+v", events, want)
	}
}

func TestBiSyncTaskEventsAndPersistenceFailure(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "left", "source file")
	putSyncFile(t, dstDir, "right", "target file")
	var events []taskEventSnapshot
	store := &singleSyncStore{failSave: true}
	err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store,
		OnEvent: taskEvents(func(event syncer.Event) {
			if _, ok := event.(syncer.ProgressEvent); ok {
				return
			}

			snapshot := snapshotTaskEvent(event.(syncer.SyncEvent))
			events = append(events, snapshot)
		}),
	})
	if !errors.Is(err, errSyncTest) {
		t.Fatalf("got %v", err)
	}
	want := []taskEventSnapshot{
		{Path: "/left", Op: syncer.OpCopy, Status: syncer.StatusStarted},
		{Path: "/left", Op: syncer.OpCopy, Status: syncer.StatusSucceeded},
		{Path: "/right", Op: syncer.OpCopy, Reverse: true, Status: syncer.StatusStarted},
		{Path: "/right", Op: syncer.OpCopy, Reverse: true, Status: syncer.StatusSucceeded},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%+v, want %+v", events, want)
	}
	// With no confirmed baseline, different current files conflict before execution.
	putSyncFile(t, srcDir, "left", "changed source")
	events = nil
	err = syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: store,
		OnEvent: taskEvents(func(event syncer.Event) {
			if _, ok := event.(syncer.ProgressEvent); ok {
				return
			}

			snapshot := snapshotTaskEvent(event.(syncer.SyncEvent))
			events = append(events, snapshot)
		}),
	})
	if !errors.Is(err, syncer.ErrSyncConflict) || len(events) != 0 {
		t.Fatalf("comparison: err=%v events=%+v", err, events)
	}
}

func TestBiSyncSkippedTaskEventsKeepDirection(t *testing.T) {
	src, dst, _, dstDir := syncPair(t)
	putSyncFile(t, dstDir, "dir/file", "reverse copy")
	var retained []syncer.SyncEvent
	err := syncer.Sync(context.Background(), &fileOperationFaultFS{FS: src}, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{},
		OnEvent: taskEvents(func(event syncer.Event) {
			if _, ok := event.(syncer.ProgressEvent); ok {
				return
			}

			retained = append(retained, event.(syncer.SyncEvent))
		}),
		OnError: func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
			return syncer.ErrorSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []taskEventSnapshot
	for _, event := range retained {
		snapshot := snapshotTaskEvent(event)
		events = append(events, snapshot)
	}
	want := []taskEventSnapshot{
		{Path: "/dir", Op: syncer.OpMkdir, Reverse: true, Status: syncer.StatusStarted},
		{Path: "/dir", Op: syncer.OpMkdir, Reverse: true, Status: syncer.StatusSkipped},
		{Path: "/dir/file", Op: syncer.OpCopy, Reverse: true, Status: syncer.StatusSkipped},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%+v, want %+v", events, want)
	}
}

func TestTwoWayEventsDescribeTask(t *testing.T) {
	for _, operation := range []string{"add", "edit", "delete"} {
		for _, reverse := range []bool{false, true} {
			name := operation + "/forward"
			if reverse {
				name = operation + "/reverse"
			}
			t.Run(name, func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				ctx := context.Background()
				option := syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: &singleSyncStore{}}
				if operation != "add" {
					putSyncFile(t, srcDir, "file", "original")
					if err := syncer.Sync(ctx, src, dst, option); err != nil {
						t.Fatal(err)
					}
				}
				changedDir := srcDir
				if reverse {
					changedDir = dstDir
				}
				if operation == "delete" {
					removeSyncPath(t, changedDir, "file")
				} else {
					putSyncFile(t, changedDir, "file", "updated contents")
				}
				var retained []syncer.SyncEvent
				option.OnEvent = taskEvents(func(event syncer.Event) {
					if _, ok := event.(syncer.ProgressEvent); ok {
						return
					}
					retained = append(retained, event.(syncer.SyncEvent))
				})
				if err := syncer.Sync(ctx, src, dst, option); err != nil {
					t.Fatal(err)
				}
				if len(retained) != 2 || retained[0].Status != syncer.StatusStarted || retained[1].Status != syncer.StatusSucceeded {
					t.Fatalf("events=%v", retained)
				}
				for _, event := range retained {
					op, content := syncer.OpCopy, "updated contents"
					if operation == "delete" {
						op, content = syncer.OpRemove, "original"
					}
					if event.FileInfo == nil || event.FileInfo.Path() != "/file" || event.FileInfo.IsDir() || event.FileInfo.Size() != int64(len(content)) || event.FileInfo.ModTime().IsZero() {
						t.Fatalf("incorrect task snapshot: %+v", event)
					}
					if event.Reverse != reverse || event.Op != op || event.Err != nil {
						t.Fatal("incorrect task details")
					}
				}
			})
		}
	}
}
