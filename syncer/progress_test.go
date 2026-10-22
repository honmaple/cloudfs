package syncer_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

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
				case syncer.SyncEvent:
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
				if info == nil || info.Path() != "/file" || info.Size() != int64(len(content)) || p.Reverse != (mode == "reverse") {
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
