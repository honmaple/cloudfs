package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/honmaple/cloudfs/syncer"
)

func TestPhaseEvents(t *testing.T) {
	for _, mode := range []syncer.Mode{syncer.ModeOneWay, syncer.ModeTwoWay} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%d/dryRun=%t", mode, dryRun), func(t *testing.T) {
				src, dst, srcDir, _ := syncPair(t)
				putSyncFile(t, srcDir, "file", "contents")
				store := &singleSyncStore{}
				var phases []syncer.PhaseEvent
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
						case syncer.PhaseEvent:
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
						case syncer.SyncEvent:
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
				var want []syncer.PhaseEvent
				for _, phase := range []syncer.Phase{syncer.PhaseScan, syncer.PhasePlan, syncer.PhaseExecute, syncer.PhaseFinish} {
					if dryRun && phase == syncer.PhaseFinish {
						break
					}
					want = append(want,
						syncer.PhaseEvent{Phase: phase, Status: syncer.StatusStarted},
						syncer.PhaseEvent{Phase: phase, Status: syncer.StatusSucceeded})
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
				failedPhase, lastPhase = syncer.PhasePlan, syncer.PhasePlan
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
			var phases []syncer.PhaseEvent
			err := syncer.Sync(context.Background(), src, dst, syncer.Option{
				StateStore: store,
				OnEvent: func(notification syncer.Event) {
					if event, ok := notification.(syncer.PhaseEvent); ok {
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
			var phases []syncer.PhaseEvent
			err := syncer.Sync(ctx, src, dst, syncer.Option{
				StateStore: store,
				OnEvent: func(notification syncer.Event) {
					if event, ok := notification.(syncer.PhaseEvent); ok {
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
	var phases []syncer.PhaseEvent
	if err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		OnEvent: func(notification syncer.Event) {
			event, ok := notification.(syncer.PhaseEvent)
			if !ok {
				t.Fatalf("empty sync reported a task: %+v", notification)
			}
			phases = append(phases, event)
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 8 || phases[7].Phase != syncer.PhaseFinish || phases[7].Status != syncer.StatusSucceeded {
		t.Fatalf("incomplete lifecycle: %+v", phases)
	}
}
