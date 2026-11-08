package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

func TestPhaseEvents(t *testing.T) {
	for _, mode := range []syncer.Mode{syncer.ModeOneWay, syncer.ModeTwoWay} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%d/dryRun=%t", mode, dryRun), func(t *testing.T) {
				src, dst, srcDir, _ := syncPair(t)
				putSyncFile(t, srcDir, "file", "contents")
				store := &singleSyncStore{}
				var phases []syncer.SyncEvent
				active := false
				var current syncer.Phase
				tasks, progress := 0, 0
				err := syncer.Sync(context.Background(), src, dst, syncer.Option{
					Mode:        mode,
					Concurrency: 2,
					DryRun:      dryRun,
					StateStore:  store,
					OnEvent: func(notification syncer.Event) {
						switch event := notification.(type) {
						case syncer.SyncEvent:
							phases = append(phases, event)
							if event.Status == syncer.StatusStarted {
								if active {
									t.Fatal("phases overlapped")
								}
								active, current = true, event.Phase
							} else {
								if !active || current != event.Phase {
									t.Fatal("phase completed without starting")
								}
								active = false
							}
						case syncer.TaskEvent:
							if !active || current != syncer.PhaseExecute {
								t.Fatal("task event outside execution phase")
							}
							tasks++
							if dryRun && event.Status != syncer.StatusPlanned {
								t.Fatal("dry run executed a task")
							}
						case syncer.ProgressEvent:
							if !active || current != syncer.PhaseExecute || dryRun {
								t.Fatal("progress outside real execution")
							}
							progress++
						}
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				var want []syncer.SyncEvent
				for _, phase := range []syncer.Phase{syncer.PhaseScan, syncer.PhasePlan, syncer.PhaseExecute, syncer.PhaseFinish} {
					if dryRun && phase == syncer.PhaseFinish {
						break
					}
					want = append(want,
						syncer.SyncEvent{Phase: phase, Status: syncer.StatusStarted},
						syncer.SyncEvent{Phase: phase, Status: syncer.StatusSucceeded})
				}
				if !reflect.DeepEqual(phases, want) || active || tasks == 0 {
					t.Fatalf("phases=%+v tasks=%d", phases, tasks)
				}
				if dryRun {
					if progress != 0 || store.saves != 0 {
						t.Fatal("dry run copied data or saved state")
					}
				} else if progress == 0 || store.saves != 1 {
					t.Fatalf("progress=%d saves=%d", progress, store.saves)
				}
			})
		}
	}
}

func TestPhaseFailures(t *testing.T) {
	for _, failure := range []string{"source-scan", "target-scan", "load", "plan", "execute", "finish"} {
		t.Run(failure, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "nested/file", "contents")
			store := &singleSyncStore{}
			failedPhase, lastPhase := syncer.PhaseScan, syncer.PhaseScan
			wantErr := errSyncTest
			switch failure {
			case "source-scan":
				src = &directoryListFault{FS: src}
			case "target-scan":
				putSyncFile(t, dstDir, "nested/file", "contents")
				dst = &directoryListFault{FS: dst}
			case "load":
				store.failLoad = true
			case "plan":
				putSyncFile(t, dstDir, "nested/file", "conflicting target")
				failedPhase, lastPhase, wantErr = syncer.PhasePlan, syncer.PhasePlan, syncer.ErrSyncConflict
			case "execute":
				src = &originalErrorFS{FS: src, err: errSyncTest}
				failedPhase, lastPhase = syncer.PhaseExecute, syncer.PhaseFinish
			case "finish":
				store.failSave = true
				failedPhase, lastPhase = syncer.PhaseFinish, syncer.PhaseFinish
			}
			var phases []syncer.SyncEvent
			err := syncer.Sync(context.Background(), src, dst, syncer.Option{
				StateStore: store,
				OnEvent: func(notification syncer.Event) {
					if event, ok := notification.(syncer.SyncEvent); ok {
						phases = append(phases, event)
					}
				},
			})
			if !errors.Is(err, wantErr) || len(phases) != 2*(int(lastPhase)+1) {
				t.Fatalf("err=%v phases=%+v", err, phases)
			}
			for index, event := range phases {
				if event.Phase != syncer.Phase(index/2) {
					t.Fatalf("wrong phase order: %+v", phases)
				}
				if index%2 == 0 {
					if event.Status != syncer.StatusStarted || event.Err != nil {
						t.Fatalf("invalid phase start: %+v", event)
					}
				} else if event.Phase == failedPhase {
					if event.Status != syncer.StatusFailed || !errors.Is(event.Err, wantErr) {
						t.Fatalf("invalid phase failure: %+v", event)
					}
				} else if event.Status != syncer.StatusSucceeded || event.Err != nil {
					t.Fatalf("invalid phase success: %+v", event)
				}
			}
			if failure == "execute" && store.saves != 1 {
				t.Fatal("execution failure did not save the confirmed directory")
			}
		})
	}
}

func TestPhaseCallbackCancellation(t *testing.T) {
	for _, phase := range []syncer.Phase{syncer.PhaseScan, syncer.PhasePlan, syncer.PhaseExecute, syncer.PhaseFinish} {
		t.Run(phase.String(), func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			putSyncFile(t, srcDir, "file", "contents")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &checkpointStore{}
			var phases []syncer.SyncEvent
			err := syncer.Sync(ctx, src, dst, syncer.Option{
				StateStore: store,
				OnEvent: func(notification syncer.Event) {
					if event, ok := notification.(syncer.SyncEvent); ok {
						phases = append(phases, event)
						if event.Phase == phase && event.Status == syncer.StatusStarted {
							cancel()
						}
					}
				},
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			if phase == syncer.PhaseFinish {
				if !store.bounded || store.saves != 1 {
					t.Fatal("cancellation prevented saving confirmed results")
				}
			} else {
				failed := phases[2*int(phase)+1]
				if failed.Status != syncer.StatusFailed || !errors.Is(failed.Err, context.Canceled) {
					t.Fatalf("missing phase cancellation: %+v", phases)
				}
			}
		})
	}
}

func TestPhaseEventsWithoutTasks(t *testing.T) {
	src, dst, _, _ := syncPair(t)
	ctx := context.Background()
	for _, fsys := range []cloudfs.FS{src, dst} {
		if err := fsys.MakeDir(ctx, "/shared"); err != nil {
			t.Fatal(err)
		}
	}
	store := &singleSyncStore{}
	var phases []syncer.SyncEvent
	if err := syncer.Sync(ctx, src, dst, syncer.Option{
		StateStore: store,
		OnEvent: func(notification syncer.Event) {
			event, ok := notification.(syncer.SyncEvent)
			if !ok {
				t.Fatalf("empty sync reported a task: %+v", notification)
			}
			if event.Phase == syncer.PhaseExecute {
				t.Fatalf("empty sync reported execution: %+v", event)
			}
			phases = append(phases, event)
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 6 || phases[5].Phase != syncer.PhaseFinish || phases[5].Status != syncer.StatusSucceeded {
		t.Fatalf("incomplete lifecycle: %+v", phases)
	}
	if store.saves != 1 {
		t.Fatalf("empty sync saved state %d times, want 1", store.saves)
	}
}

// taskEvents keeps task-focused tests independent of lifecycle notifications.
func taskEvents(handler syncer.EventHandler) syncer.EventHandler {
	return func(event syncer.Event) {
		if _, ok := event.(syncer.SyncEvent); ok {
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

func snapshotTaskEvent(event syncer.TaskEvent) taskEventSnapshot {
	return taskEventSnapshot{
		Path:    event.FileInfo.Path(),
		Op:      event.Op,
		Reverse: event.Reverse,
		NewPath: event.NewPath,
		Status:  event.Status,
		Err:     event.Err,
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
		events = append(events, snapshotTaskEvent(event.(syncer.TaskEvent)))
	}
	want := []taskEventSnapshot{
		{Path: "dir", Op: syncer.OpMkdir, Status: syncer.StatusStarted},
		{Path: "dir", Op: syncer.OpMkdir, Status: syncer.StatusSucceeded},
		{Path: "dir/file", Op: syncer.OpCopy, Status: syncer.StatusStarted},
		{Path: "dir/file", Op: syncer.OpCopy, Status: syncer.StatusSucceeded},
		{Path: "old", Op: syncer.OpRmdir, Status: syncer.StatusStarted},
		{Path: "old", Op: syncer.OpRmdir, Status: syncer.StatusSucceeded},
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
				events = append(events, snapshotTaskEvent(event.(syncer.TaskEvent)))
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
			if len(events) != 2 || events[0].Status != syncer.StatusStarted || events[1].Path != "file" || events[1].Op != syncer.OpCopy {
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
			events = append(events, snapshotTaskEvent(event.(syncer.TaskEvent)))
		}),
		OnError: func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
			return syncer.ErrorSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []taskEventSnapshot{
		{Path: "dir", Op: syncer.OpMkdir, Status: syncer.StatusStarted},
		{Path: "dir", Op: syncer.OpMkdir, Status: syncer.StatusSkipped},
		{Path: "dir/file", Op: syncer.OpCopy, Status: syncer.StatusSkipped},
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

			snapshot := snapshotTaskEvent(event.(syncer.TaskEvent))
			events = append(events, snapshot)
		}),
	})
	if !errors.Is(err, errSyncTest) {
		t.Fatalf("got %v", err)
	}
	want := []taskEventSnapshot{
		{Path: "left", Op: syncer.OpCopy, Status: syncer.StatusStarted},
		{Path: "left", Op: syncer.OpCopy, Status: syncer.StatusSucceeded},
		{Path: "right", Op: syncer.OpCopy, Reverse: true, Status: syncer.StatusStarted},
		{Path: "right", Op: syncer.OpCopy, Reverse: true, Status: syncer.StatusSucceeded},
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

			snapshot := snapshotTaskEvent(event.(syncer.TaskEvent))
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
	var retained []syncer.TaskEvent
	err := syncer.Sync(context.Background(), &fileOperationFaultFS{FS: src}, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{},
		OnEvent: taskEvents(func(event syncer.Event) {
			if _, ok := event.(syncer.ProgressEvent); ok {
				return
			}

			retained = append(retained, event.(syncer.TaskEvent))
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
		{Path: "dir", Op: syncer.OpMkdir, Reverse: true, Status: syncer.StatusStarted},
		{Path: "dir", Op: syncer.OpMkdir, Reverse: true, Status: syncer.StatusSkipped},
		{Path: "dir/file", Op: syncer.OpCopy, Reverse: true, Status: syncer.StatusSkipped},
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
				var retained []syncer.TaskEvent
				option.OnEvent = taskEvents(func(event syncer.Event) {
					if _, ok := event.(syncer.ProgressEvent); ok {
						return
					}
					retained = append(retained, event.(syncer.TaskEvent))
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
					if event.FileInfo == nil || event.FileInfo.Path() != "file" || event.FileInfo.IsDir() || event.FileInfo.Size() != int64(len(content)) || event.FileInfo.ModTime().IsZero() {
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

func TestCopyProgressRetriesAndDirection(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, _ := syncPair(t)
			content := strings.Repeat("x", 100000)
			putSyncFile(t, srcDir, "file", content)
			fault := &sourceReadFault{
				FS:                src,
				remainingFailures: 1,
			}
			var samples []syncer.ProgressEvent
			started, finished := false, false
			handler := taskEvents(func(event syncer.Event) {
				switch event := event.(type) {
				case syncer.TaskEvent:
					if event.Status == syncer.StatusStarted {
						started = true
					} else {
						finished = true
					}
				case syncer.ProgressEvent:
					if !started || finished {
						t.Fatal("progress outside task lifecycle")
					}
					samples = append(samples, event)
				}
			})
			retry := func(ctx context.Context, _ syncer.FileError) (syncer.ErrorPolicy, error) {
				return syncer.ErrorRetry, nil
			}
			var err error
			if mode == "sync" {
				err = syncer.Sync(context.Background(), fault, dst, syncer.Option{
					OnEvent: handler,
					OnError: retry,
				})
			} else {
				left, right := cloudfs.FS(fault), dst
				if mode == "reverse" {
					left, right = dst, fault
				}
				err = syncer.Sync(context.Background(), left, right, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: &singleSyncStore{},
					OnEvent:    handler,
					OnError:    retry,
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			last, attempt := int64(-1), 0
			for _, p := range samples {
				info := p.FileInfo
				if info == nil || info.Path() != "file" || info.Size() != int64(len(content)) || p.Reverse != (mode == "reverse") {
					t.Fatalf("invalid snapshot: %+v", p)
				}
				if p.Written == 0 {
					attempt++
					last = -1
				}
				if p.Written <= last {
					t.Fatalf("nonmonotonic progress: %+v", p)
				}
				last = p.Written
			}
			if !finished || attempt != 2 || last != int64(len(content)) {
				t.Fatalf("attempt=%d final=%d", attempt, last)
			}
		})
	}
}

func TestProgressDoesNotConfirmCloseFailure(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "data")
	store := &singleSyncStore{}
	var last int64
	err := syncer.Sync(context.Background(), src, closeFailureFS{
		dst,
	}, syncer.Option{

		StateStore: store,
		OnEvent: taskEvents(func(event syncer.Event) {
			if p, ok := event.(syncer.ProgressEvent); ok {
				last = p.Written
			}
		}),
	})
	if !errors.Is(err, errSyncTest) || last != 4 || store.saves != 0 {
		t.Fatalf("error=%v progress=%d saves=%d", err, last, store.saves)
	}
}
