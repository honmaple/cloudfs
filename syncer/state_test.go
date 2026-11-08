package syncer_test

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/syncer"
)

func TestStateRevisionDependsOnContent(t *testing.T) {
	first := &syncer.State{
		Version: 1, HashAlgorithm: crypto.SHA256, Revision: "previous-uuid", UpdatedAt: time.Unix(1000, 0).UTC(),
		Source: map[string]*syncer.Record{"a": {Size: 1}, "b": {Size: 2}},
		Target: map[string]*syncer.Record{},
	}
	second := &syncer.State{
		Version: 1, HashAlgorithm: crypto.SHA256, Revision: "another-uuid", UpdatedAt: time.Unix(2000, 0).UTC(),
		Source: make(map[string]*syncer.Record),
		Target: map[string]*syncer.Record{},
	}
	second.Source["b"] = &syncer.Record{Size: 2}
	second.Source["a"] = &syncer.Record{Size: 1}
	if err := first.UpdateRevision(); err != nil {
		t.Fatal(err)
	}
	if err := second.UpdateRevision(); err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(first.Revision)
	if err != nil || len(decoded) != sha256.Size || first.Revision != second.Revision {
		t.Fatalf("revisions differ by insertion order, previous ID, or update time: %q/%q", first.Revision, second.Revision)
	}
	for _, field := range []string{"version", "algorithm", "source", "target"} {
		t.Run(field, func(t *testing.T) {
			state := &syncer.State{Version: first.Version, HashAlgorithm: first.HashAlgorithm, Source: first.Source, Target: first.Target}
			switch field {
			case "algorithm":
				state.HashAlgorithm = crypto.MD5
			case "version":
				state.Version = 2
			case "source":
				state.Source = map[string]*syncer.Record{"a": {Size: 3}, "b": {Size: 2}}
			case "target":
				state.Target = map[string]*syncer.Record{"a": {Size: 1}}
			}
			if err := state.UpdateRevision(); err != nil {
				t.Fatal(err)
			}
			if state.Revision == first.Revision {
				t.Fatalf("%s change did not change revision", field)
			}
		})
	}
}

func TestStatePreservesStoredJSONAndRevision(t *testing.T) {
	const stored = `{"source":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}},"target":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}},"version":1,"revision":"existing-revision","updated_at":"2026-10-23T00:00:00Z","hash_algorithm":5}`
	var state syncer.State
	if err := json.Unmarshal([]byte(stored), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Valid() {
		t.Fatal("existing state rejected")
	}
	encoded, err := json.Marshal(&state)
	if err != nil || string(encoded) != stored {
		t.Fatalf("stored format changed: %s %v", encoded, err)
	}
	if err := state.UpdateRevision(); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"hash_algorithm":5,"version":1,"source":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}},"target":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}}}`)
	expected := sha256.Sum256(content)
	if state.Revision != hex.EncodeToString(expected[:]) {
		t.Fatal("revision calculation changed")
	}
}

func TestFileStoreReadsExistingEnvelope(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "state.json")
	const stored = `{"source":"left","target":"right","metadata":{"hash_algorithm":5,"version":1,"revision":"existing-revision","source":{},"target":{}}}`
	if err := os.WriteFile(filename, []byte(stored), 0600); err != nil {
		t.Fatal(err)
	}
	store := syncer.NewFileStore(filename, "left", "right")
	state, err := store.Load(context.Background())
	if err != nil || !state.Valid() {
		t.Fatalf("existing state rejected: %v", err)
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["metadata"] == nil || fields["state"] != nil {
		t.Fatalf("envelope changed: %s", data)
	}
}

func TestStateCleansHistoryAfterBothSidesReplaceDirectory(t *testing.T) {
	for _, mode := range []string{"sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			store := &singleSyncStore{}
			tasks := 0
			onEvent := taskEvents(func(event syncer.Event) {
				switch value := event.(type) {
				case syncer.TaskEvent:
					if value.Status == syncer.StatusStarted {
						tasks++
					}
				}
			})
			run := func() {
				t.Helper()
				var err error
				if mode == "sync" {
					err = syncer.Sync(context.Background(), src, dst, syncer.Option{
						Checksum:   true,
						StateStore: store,
						OnEvent:    onEvent,
					})
				} else {
					err = syncer.Sync(context.Background(), src, dst, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						Checksum:   true,
						StateStore: store,
						OnEvent:    onEvent,
					})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			putSyncFile(t, srcDir, "dir/nested/file", "old")
			run()
			for _, root := range []string{srcDir, dstDir} {
				removeSyncPath(t, root, "dir")
				putSyncFile(t, root, "dir", "new")
			}
			tasks = 0
			run()
			if tasks != 0 {
				t.Fatal("equal files required operations")
			}
			state, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !state.Valid() {
				t.Fatal("historical descendants made the saved state invalid")
			}
			for _, records := range []map[string]*syncer.Record{state.Source, state.Target} {
				if len(records) != 1 || records["dir"] == nil || records["dir"].IsDir {
					t.Fatalf("historical descendants were not removed: %v", records)
				}
			}
		})
	}
}

func TestCancellationSavesConfirmedResults(t *testing.T) {
	for _, mode := range []string{"sync", "bisync", "reverse"} {
		for _, stop := range []string{"a", "b"} {
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
						case syncer.TaskEvent:
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
					if stop == "a" {
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
						case syncer.TaskEvent:
							event = snapshotTaskEvent(value)
						default:
							return
						}
						if event.Path == "a" || stop == "b" {
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
					if event, ok := notification.(syncer.TaskEvent); ok && event.Op == stop && event.Status == syncer.StatusSucceeded {
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
			if event, ok := notification.(syncer.ProgressEvent); ok && event.FileInfo.Path() == "b" && event.Written > 0 {
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
				if event, ok := notification.(syncer.TaskEvent); ok && event.Status == syncer.StatusSucceeded {
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
