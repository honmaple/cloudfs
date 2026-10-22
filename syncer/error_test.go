package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

type originalErrorFS struct {
	cloudfs.FS
	err error
}

func (f *originalErrorFS) Open(context.Context, string) (cloudfs.File, error) { return nil, f.err }

func TestSyncPreservesDriverAndCallbackErrors(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		for _, custom := range []bool{false, true} {
			name := mode + "/driver"
			if custom {
				name = mode + "/callback"
			}
			t.Run(name, func(t *testing.T) {
				src, dst, srcDir, _ := syncPair(t)
				putSyncFile(t, srcDir, "file", "data")
				original := &fs.PathError{Op: "open", Path: "/provider/file", Err: fs.ErrPermission}
				var want error = original
				if custom {
					want = errors.New("caller-defined error")
				}
				called := false
				handler := func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
					called = true
					if failure.Op != syncer.OpCopy || failure.Reverse != (mode == "reverse") || failure.Attempt != 1 || failure.NewPath != "" {
						t.Fatalf("incorrect operation context: %+v", failure)
					}
					var pathErr *fs.PathError
					if failure.Error() != original.Error() || !errors.As(failure, &pathErr) || pathErr != original {
						t.Fatal("FileError changed or lost its underlying error")
					}
					if failure.Err != original || failure.FileInfo.Path() != "/file" {
						t.Fatalf("callback changed driver error: %v", failure.Err)
					}
					if custom {
						return syncer.ErrorRetry, want
					}
					return syncer.ErrorAbort, nil
				}
				var eventErr error
				event := taskEvents(func(event syncer.Event) {
					switch e := event.(type) {
					case syncer.SyncEvent:
						if e.Status == syncer.StatusFailed {
							eventErr = e.Err
						}
					}
				})
				var err error
				fault := &originalErrorFS{FS: src, err: original}
				if mode == "sync" {
					err = syncer.Sync(context.Background(), fault, dst, syncer.Option{OnError: handler, OnEvent: event})
				} else {
					left, right := cloudfs.FS(fault), dst
					if mode == "reverse" {
						left, right = dst, fault
					}
					err = syncer.Sync(context.Background(), left, right, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						StateStore: &singleSyncStore{}, OnError: handler, OnEvent: event})
				}
				if !called || !errors.Is(err, want) || err.Error() != want.Error() || eventErr != want {
					t.Fatalf("returned=%v event=%v called=%t", err, eventErr, called)
				}
				if !custom {
					var pathErr *fs.PathError
					if !errors.As(err, &pathErr) || pathErr != original {
						t.Fatal("lost original PathError")
					}
				}
			})
		}
	}
}

func TestSyncRejectsInvalidErrorPolicy(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	putSyncFile(t, srcDir, "file", "contents")
	calls := 0
	err := syncer.Sync(context.Background(), &originalErrorFS{FS: src, err: fs.ErrPermission}, dst, syncer.Option{
		OnError: func(context.Context, syncer.FileError) (syncer.ErrorPolicy, error) {
			calls++
			return syncer.ErrorPolicy(255), nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid error policy 255") || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestSyncMaxRetry(t *testing.T) {
	for _, mode := range []syncer.Mode{syncer.ModeOneWay, syncer.ModeTwoWay} {
		for _, limit := range []int{-1, 0, 1, 2} {
			t.Run(fmt.Sprintf("mode=%d/limit=%d", mode, limit), func(t *testing.T) {
				src, dst, srcDir, _ := syncPair(t)
				putSyncFile(t, srcDir, "file", "contents")
				calls := 0
				err := syncer.Sync(context.Background(), &originalErrorFS{FS: src, err: syncer.ErrSourceChanged}, dst, syncer.Option{
					Mode:       mode,
					MaxRetry:   limit,
					StateStore: &singleSyncStore{},
					OnError: func(_ context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
						calls++
						if failure.Attempt != calls || !errors.Is(failure, syncer.ErrSourceChanged) {
							t.Fatalf("incorrect failed attempt: %+v", failure)
						}
						return syncer.ErrorRetry, nil
					},
				})
				wantRetries := limit
				if limit <= 0 {
					wantRetries = 3
				}
				if !errors.Is(err, syncer.ErrSourceChanged) || calls != wantRetries+1 {
					t.Fatalf("err=%v attempts=%d, want %d", err, calls, wantRetries+1)
				}
			})
		}
	}
}
