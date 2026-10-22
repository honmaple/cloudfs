package syncer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/syncer"
)

func TestStateRevisionDependsOnContent(t *testing.T) {
	first := &syncer.State{
		Type: "sync", Version: 1, Revision: "previous-uuid", UpdatedAt: time.Unix(1000, 0).UTC(),
		Source: map[string]*syncer.Entry{"a": {Size: 1}, "b": {Size: 2}},
		Target: map[string]*syncer.Entry{},
	}
	second := &syncer.State{
		Type: "sync", Version: 1, Revision: "another-uuid", UpdatedAt: time.Unix(2000, 0).UTC(),
		Source: make(map[string]*syncer.Entry),
		Target: map[string]*syncer.Entry{},
	}
	second.Source["b"] = &syncer.Entry{Size: 2}
	second.Source["a"] = &syncer.Entry{Size: 1}
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
	for _, field := range []string{"type", "version", "source", "target"} {
		t.Run(field, func(t *testing.T) {
			state := &syncer.State{Type: first.Type, Version: first.Version, Source: first.Source, Target: first.Target}
			switch field {
			case "type":
				state.Type = "bisync"
			case "version":
				state.Version = 2
			case "source":
				state.Source = map[string]*syncer.Entry{"a": {Size: 3}, "b": {Size: 2}}
			case "target":
				state.Target = map[string]*syncer.Entry{"a": {Size: 1}}
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
	const stored = `{"type":"sync","version":1,"revision":"existing-revision","updated_at":"2026-10-23T00:00:00Z","source":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}},"target":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}}}`
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
	content := []byte(`{"type":"sync","version":1,"source":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}},"target":{"file":{"size":4,"mod_time":"0001-01-01T00:00:00Z"}}}`)
	expected := sha256.Sum256(content)
	if state.Revision != hex.EncodeToString(expected[:]) {
		t.Fatal("revision calculation changed")
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
				case syncer.SyncEvent:
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
			for _, entries := range []map[string]*syncer.Entry{state.Source, state.Target} {
				if len(entries) != 1 || entries["dir"] == nil || entries["dir"].IsDir {
					t.Fatalf("historical descendants were not removed: %v", entries)
				}
			}
		})
	}
}
