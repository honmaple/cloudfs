package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

func TestParallelCopiesRespectLimitAndBarriers(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "dir/a", "first")
			putSyncFile(t, srcDir, "dir/b", "second")
			putSyncFile(t, srcDir, "dir/nested/c", "third")
			putSyncFile(t, dstDir, "dir", "replaced file")
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			target := &parallelGateFS{
				FS:      dst,
				started: make(chan string, 8),
				release: release,
			}
			var callbackActive atomic.Int32
			handler := taskEvents(func(syncer.Event) {
				if callbackActive.Add(1) != 1 {
					t.Error("callbacks ran concurrently")
				}
				runtime.Gosched()
				callbackActive.Add(-1)
			})
			done := make(chan error, 1)
			go func() {
				if mode == "sync" {
					done <- syncer.Sync(context.Background(), src, target, syncer.Option{
						Concurrency: 2,
						OnConflict:  syncer.PreferSrc(),
						OnEvent:     handler,
					})
					return
				}
				left, right, policy := cloudfs.FS(src), cloudfs.FS(target), syncer.ConflictPreferSrc
				if mode == "reverse" {
					left, right, policy = right, left, syncer.ConflictPreferDst
				}
				done <- syncer.Sync(context.Background(), left, right, syncer.Option{
					Mode:        syncer.ModeTwoWay,
					Concurrency: 2,
					StateStore:  &singleSyncStore{},
					OnConflict:  fixedConflict(policy),
					OnEvent:     handler,
				})
			}()
			for range 2 {
				select {
				case <-target.started:
				case err := <-done:
					t.Fatalf("copies did not overlap: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("copies did not overlap")
				}
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("parallel copies did not finish")
			}
			if target.maximum.Load() != 2 || target.active.Load() != 0 {
				t.Fatalf("maximum=%d active=%d", target.maximum.Load(), target.active.Load())
			}
			wantSyncFile(t, dstDir, "dir/a", "first")
			wantSyncFile(t, dstDir, "dir/b", "second")
			wantSyncFile(t, dstDir, "dir/nested/c", "third")
		})
	}
}

func TestParallelFailureWaitsForInflightCopies(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	for _, path := range []string{"a", "b", "c"} {
		putSyncFile(t, srcDir, path, "contents of "+path)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	target := &parallelGateFS{
		FS:      dst,
		started: make(chan string, 8),
		release: release,
		fail: map[string]error{
			"/a": errSyncTest,
		},
	}
	store := &singleSyncStore{}
	done := make(chan error, 1)
	go func() {
		done <- syncer.Sync(context.Background(), src, target, syncer.Option{
			Mode:        syncer.ModeTwoWay,
			Concurrency: 2,
			StateStore:  store,
		})
	}()
	for range 2 {
		select {
		case <-target.started:
		case <-time.After(5 * time.Second):
			t.Fatal("first batch did not start")
		}
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, errSyncTest) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed batch did not finish")
	}
	wantSyncAbsent(t, dstDir, "a")
	wantSyncFile(t, dstDir, "b", "contents of b")
	state, err := store.Load(context.Background())
	if err != nil || state == nil || !state.Valid() || state.Source["b"] == nil || state.Target["b"] == nil || state.Target["a"] != nil {
		t.Fatalf("incorrect partial successes: %+v, %v", state, err)
	}
}

func TestParallelRetriesCanReportProgress(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	for _, path := range []string{"a", "b", "c"} {
		putSyncFile(t, srcDir, path, "contents of "+path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Deliberately shared without test-side locks: callback serialization must
	// protect these values across error callbacks and subsequent retry progress.
	callbacks, progress := 0, 0
	retries := make(map[string]int)
	done := make(chan error, 1)
	go func() {
		done <- syncer.Sync(ctx, &parallelReadFaultFS{FS: src}, dst, syncer.Option{
			Mode:        syncer.ModeTwoWay,
			Concurrency: 3,
			MaxRetry:    1,
			StateStore:  &singleSyncStore{},
			OnEvent: taskEvents(func(notification syncer.Event) {
				callbacks++
				runtime.Gosched()
				if event, ok := notification.(syncer.ProgressEvent); ok && event.Written > 0 {
					progress++
				}
			}),
			OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
				callbacks++
				runtime.Gosched()
				retries[failure.FileInfo.Path()]++
				return syncer.ErrorRetry, nil
			},
		})
	}()
	select {
	case err := <-done:
		if err != nil || callbacks == 0 || progress != 3 || len(retries) != 3 {
			t.Fatalf("err=%v callbacks=%d progress=%d retries=%v", err, callbacks, progress, retries)
		}
	case <-ctx.Done():
		t.Fatal("retry progress blocked callback delivery")
	}
	for _, path := range []string{"a", "b", "c"} {
		if retries["/"+path] != 1 {
			t.Fatalf("unexpected retries: %v", retries)
		}
		wantSyncFile(t, dstDir, path, "contents of "+path)
	}
}

func TestParallelCancellationSavesCompletedCopies(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			for _, path := range []string{"a", "b", "c"} {
				putSyncFile(t, srcDir, path, "contents of "+path)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &checkpointStore{}
			completed := make(map[string]bool)
			handler := taskEvents(func(notification syncer.Event) {
				status := syncer.Status(255)
				var path string
				switch event := notification.(type) {
				case syncer.SyncEvent:
					status, path = event.Status, event.FileInfo.Path()
				}
				if status == syncer.StatusSucceeded {
					completed[path] = true
					if len(completed) == 2 {
						cancel()
					}
				}
			})
			var err error
			if mode == "sync" {
				err = syncer.Sync(ctx, src, dst, syncer.Option{
					Concurrency: 2,

					StateStore: store,
					OnEvent:    handler,
				})
			} else {
				left, right := src, dst
				if mode == "reverse" {
					left, right = dst, src
				}
				err = syncer.Sync(ctx, left, right, syncer.Option{
					Mode:        syncer.ModeTwoWay,
					Concurrency: 2,
					StateStore:  store,
					OnEvent:     handler,
				})
			}
			if !errors.Is(err, context.Canceled) || len(completed) < 2 || len(completed) > 3 || !store.bounded {
				t.Fatalf("err=%v completed=%v bounded-save=%t", err, completed, store.bounded)
			}
			state, err := store.Load(context.Background())
			if err != nil || state == nil || !state.Valid() || len(state.Source) != len(completed) || len(state.Target) != len(completed) {
				t.Fatalf("invalid completed batch: %+v, %v", state, err)
			}
			for _, path := range []string{"a", "b", "c"} {
				if completed["/"+path] {
					wantSyncFile(t, dstDir, path, "contents of "+path)
					if state.Source[path] == nil || state.Target[path] == nil {
						t.Fatalf("missing completed state for %s", path)
					}
				} else if state.Source[path] != nil || state.Target[path] != nil {
					t.Fatalf("unconfirmed state for %s", path)
				}
			}
		})
	}
}

func TestInvalidConcurrency(t *testing.T) {
	if err := syncer.Sync(context.Background(), nil, nil, syncer.Option{
		Concurrency: -1,
	}); err == nil {
		t.Fatal("Sync accepted negative concurrency")
	}
	if err := syncer.Sync(context.Background(), nil, nil, syncer.Option{
		Mode:        syncer.ModeTwoWay,
		Concurrency: -1,
	}); err == nil {
		t.Fatal("BiSync accepted negative concurrency")
	}
}

func TestParallelCopiesReturnAllBatchErrors(t *testing.T) {
	src, dst, srcDir, _ := syncPair(t)
	for _, path := range []string{"a", "b", "c"} {
		putSyncFile(t, srcDir, path, "contents of "+path)
	}
	first, second := errors.New("first failure"), errors.New("second failure")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	target := &parallelGateFS{
		FS:      dst,
		started: make(chan string, 8),
		release: release,
		fail: map[string]error{
			"/a": first,
			"/b": second,
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- syncer.Sync(context.Background(), src, target, syncer.Option{
			Concurrency: 2,
		})
	}()
	for range 2 {
		select {
		case <-target.started:
		case <-time.After(5 * time.Second):
			t.Fatal("copies did not start together")
		}
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, first) || !errors.Is(err, second) || len(target.started) != 0 {
			t.Fatalf("err=%v additional copies=%d", err, len(target.started))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed copies did not finish")
	}
}

type parallelReadFaultFS struct {
	cloudfs.FS
	opened sync.Map
}

func (fsys *parallelReadFaultFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	file, err := fsys.FS.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	if _, seen := fsys.opened.LoadOrStore(path, true); !seen {
		return &parallelReadFaultFile{
			File: file,
		}, nil
	}
	return file, nil
}

type parallelReadFaultFile struct {
	cloudfs.File
}

func (*parallelReadFaultFile) Read([]byte) (int, error) {
	return 0, errSyncTest
}

type parallelGateFS struct {
	cloudfs.FS
	started     chan string
	release     <-chan struct{}
	blockedPath string
	fail        map[string]error
	active      atomic.Int32
	maximum     atomic.Int32
}

func (fsys *parallelGateFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	active := fsys.active.Add(1)
	for previous := fsys.maximum.Load(); active > previous; previous = fsys.maximum.Load() {
		if fsys.maximum.CompareAndSwap(previous, active) {
			break
		}
	}
	fsys.started <- path
	if fsys.blockedPath == "" || fsys.blockedPath == path {
		select {
		case <-fsys.release:
		case <-ctx.Done():
			fsys.active.Add(-1)
			return nil, ctx.Err()
		}
	}
	if err := fsys.fail[path]; err != nil {
		fsys.active.Add(-1)
		return nil, err
	}
	writer, err := fsys.FS.Create(ctx, path)
	if err != nil {
		fsys.active.Add(-1)
		return nil, err
	}
	return &parallelGateWriter{
		FileWriter: writer,
		active:     &fsys.active,
	}, nil
}

func (fsys *parallelGateFS) MakeDir(ctx context.Context, path string) error {
	if fsys.active.Load() != 0 {
		return fmt.Errorf("mkdir overlapped active copies: %s", path)
	}
	return fsys.FS.MakeDir(ctx, path)
}

func (fsys *parallelGateFS) Remove(ctx context.Context, path string) error {
	if fsys.active.Load() != 0 {
		return fmt.Errorf("remove overlapped active copies: %s", path)
	}
	return fsys.FS.Remove(ctx, path)
}

type parallelGateWriter struct {
	cloudfs.FileWriter
	active *atomic.Int32
}

func (writer *parallelGateWriter) Close() error {
	defer writer.active.Add(-1)
	return writer.FileWriter.Close()
}

func TestParallelCopiesRefillBeforeSlowCopyFinishes(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	for _, path := range []string{"a", "b", "c"} {
		putSyncFile(t, srcDir, path, path)
	}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	target := &parallelGateFS{
		FS:          dst,
		started:     make(chan string, 3),
		release:     release,
		blockedPath: "/a",
	}
	done := make(chan error, 1)
	go func() {
		done <- syncer.Sync(context.Background(), src, target, syncer.Option{Concurrency: 2})
	}()
	started := make(map[string]bool)
	for !started["/a"] || !started["/c"] {
		select {
		case path := <-target.started:
			started[path] = true
		case err := <-done:
			t.Fatalf("sync ended before blocked copy was released: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("next copy waited for the slow copy")
		}
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copies did not finish")
	}
	if target.maximum.Load() != 2 {
		t.Fatalf("maximum concurrency=%d", target.maximum.Load())
	}
	for _, path := range []string{"a", "b", "c"} {
		wantSyncFile(t, dstDir, path, path)
	}
}

func TestSyncStagesDeleteLast(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "a/deep/file", "nested")
	putSyncFile(t, srcDir, "z/file", "other")
	putSyncFile(t, dstDir, "gone", "delete file")
	putSyncFile(t, dstDir, "obsolete/file", "delete directory")
	var planned, executed []taskEventSnapshot
	option := syncer.Option{
		Concurrency: 2,
		Delete:      true,
		DryRun:      true,
		OnEvent: taskEvents(func(notification syncer.Event) {
			if event, ok := notification.(syncer.SyncEvent); ok {
				planned = append(planned, snapshotTaskEvent(event))
			}
		}),
	}
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	option.DryRun = false
	option.OnEvent = taskEvents(func(notification syncer.Event) {
		if event, ok := notification.(syncer.SyncEvent); ok && event.Status == syncer.StatusStarted {
			executed = append(executed, snapshotTaskEvent(event))
		}
	})
	if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
		t.Fatal(err)
	}
	for _, events := range [][]taskEventSnapshot{planned, executed} {
		if len(events) != 7 || events[0].Path != "/a" || events[1].Path != "/z" || events[2].Path != "/a/deep" {
			t.Fatalf("parent directories were not created first: %+v", events)
		}
		for i, op := range []syncer.Op{syncer.OpMkdir, syncer.OpMkdir, syncer.OpMkdir, syncer.OpCopy, syncer.OpCopy, syncer.OpRemove, syncer.OpRmdir} {
			if events[i].Op != op {
				t.Fatalf("unexpected stage order: %+v", events)
			}
		}
	}
	wantSyncAbsent(t, dstDir, "gone")
	wantSyncAbsent(t, dstDir, "obsolete")
	wantSyncFile(t, dstDir, "a/deep/file", "nested")
	wantSyncFile(t, dstDir, "z/file", "other")
}

func TestCopyFailureStopsFinalDeletions(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "copy")
	putSyncFile(t, dstDir, "gone", "keep after failure")
	putSyncFile(t, dstDir, "obsolete/file", "keep directory after failure")
	err := syncer.Sync(context.Background(), src, &conflictCopyFailureFS{
		FS:   dst,
		path: "/file",
	}, syncer.Option{
		Concurrency: 2,
		Delete:      true,
	})
	if !errors.Is(err, errSyncTest) {
		t.Fatalf("got %v", err)
	}
	wantSyncFile(t, dstDir, "gone", "keep after failure")
	wantSyncFile(t, dstDir, "obsolete/file", "keep directory after failure")
}

func TestKeepBothCopiesCanOverlap(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "file", "source")
	putSyncFile(t, dstDir, "file", "target version")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	left := &parallelGateFS{
		FS:          src,
		started:     make(chan string, 1),
		release:     release,
		blockedPath: "/file.conflict",
	}
	copiedOriginal := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- syncer.Sync(context.Background(), left, dst, syncer.Option{
			Mode:        syncer.ModeTwoWay,
			Concurrency: 2,
			StateStore:  &singleSyncStore{},
			OnConflict:  syncer.KeepBoth(),
			OnEvent: taskEvents(func(notification syncer.Event) {
				if event, ok := notification.(syncer.SyncEvent); ok && event.Op == syncer.OpCopy && event.FileInfo.Path() == "/file" && event.Status == syncer.StatusSucceeded {
					close(copiedOriginal)
				}
			}),
		})
	}()
	select {
	case <-copiedOriginal:
		wantSyncFile(t, dstDir, "file", "source")
		wantSyncFile(t, dstDir, "file.conflict", "target version")
	case err := <-done:
		t.Fatalf("sync ended before backup copy was released: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("original copy waited for backup copy")
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backup copy did not finish")
	}
	wantSyncFile(t, srcDir, "file.conflict", "target version")
}
