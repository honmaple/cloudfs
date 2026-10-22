package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	stdpath "path"
	"strings"
	"time"
)

// Entry describes a file or directory in a synchronization snapshot or State.
// Paths are the keys of State.Source and State.Target.
type Entry struct {
	IsDir   bool      `json:"is_dir,omitempty"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
	Hash    string    `json:"hash,omitempty"`
}

// Valid reports whether the entry has valid synchronization metadata.
// A nil entry is invalid. Directories must have zero size, time and hash;
// nonempty file hashes must be valid SHA-256 hex digests. It performs no I/O.
func (entry *Entry) Valid() bool {
	if entry == nil {
		return false
	}
	if entry.IsDir {
		return entry.Size == 0 && entry.ModTime.IsZero() && entry.Hash == ""
	}
	if entry.Hash == "" {
		return true
	}
	hash, err := hex.DecodeString(entry.Hash)
	return err == nil && len(hash) == sha256.Size
}

// Equal compares synchronization information without reading file contents.
// Two nil entries are equal. Directories compare only by type, not their subtrees.
// Files compare hashes when both are nonempty, otherwise size and modification time.
func (entry *Entry) Equal(other *Entry) bool {
	if entry == nil || other == nil {
		return entry == other
	}
	if entry.IsDir != other.IsDir {
		return false
	}
	if entry.IsDir {
		return true
	}
	if entry.Hash != "" && other.Hash != "" {
		return entry.Hash == other.Hash
	}
	return entry.Size == other.Size && entry.ModTime.Equal(other.ModTime)
}

// FileInfo adapts an entry to a read-only view at an absolute path within the supplied FS.
// A nil entry returns nil. The returned snapshot is independent of later entry changes.
func (entry *Entry) FileInfo(path string) FileInfo {
	if entry == nil {
		return nil
	}
	return &fileInfo{entry: *entry, path: path}
}

// withHash returns a confirmed source snapshot without changing scanned entries.
func (entry *Entry) withHash(hash string) *Entry {
	if entry == nil || entry.IsDir || entry.Hash == hash {
		return entry
	}
	confirmed := *entry
	confirmed.Hash = hash
	return &confirmed
}

// StateStore persists state for one fixed pair of supplied filesystems.
// Load returns nil on the first run. Implementations own serialization and must
// treat loaded and saved state as immutable.
// Save atomically replaces state; failure must leave an intact old or new value.
// If cancellation precedes saving, confirmed results are saved using a context
// with the caller's values and a separate five-second deadline. Honor that context.
// The caller owns durability and exclusive access to the filesystem pair.
type StateStore interface {
	Load(context.Context) (*State, error)
	Save(context.Context, *State) error
}

// State contains the confirmed per-side file metadata used as a historical
// comparison baseline. It may include partial successes, not execution progress.
// Its exported fields support serialization, for example with encoding/json.
// Treat its contents as immutable once supplied to or received from Sync.
type State struct {
	Type      string            `json:"type"`
	Version   int               `json:"version"`    // Protocol version; currently 1.
	Revision  string            `json:"revision"`   // Identifies the state content.
	UpdatedAt time.Time         `json:"updated_at"` // UTC time when changed state was prepared for saving.
	Source    map[string]*Entry `json:"source"`
	Target    map[string]*Entry `json:"target"`
}

// withPathPrefix converts state keys at the storage boundary without modifying
// maps held by the caller. Internal keys use "/"; persisted keys use "".
func (state *State) withPathPrefix(prefix string) *State {
	convert := func(entries map[string]*Entry) map[string]*Entry {
		converted := make(map[string]*Entry, len(entries))
		for path, entry := range entries {
			converted[prefix+strings.TrimPrefix(path, "/")] = entry
		}
		return converted
	}
	converted := *state
	converted.Source = convert(state.Source)
	converted.Target = convert(state.Target)
	return &converted
}

// Valid reports whether state has a supported protocol version, a nonempty
// revision and valid entry maps, paths and directory parents. Nil is invalid.
// It does not recompute Revision or read the filesystems.
func (state *State) Valid() bool {
	if state == nil || state.Version != 1 || state.Revision == "" || (state.Type != "sync" && state.Type != "bisync") {
		return false
	}
	validEntries := func(entries map[string]*Entry) bool {
		if entries == nil {
			return false
		}
		for path, entry := range entries {
			if entry == nil || !fs.ValidPath(path) || path == "." || !entry.Valid() {
				return false
			}
			if parentPath := stdpath.Dir(path); parentPath != "." {
				if parent := entries[parentPath]; parent == nil || !parent.IsDir {
					return false
				}
			}
		}
		return true
	}
	return validEntries(state.Source) && validEntries(state.Target)
}

// UpdateRevision replaces Revision with the SHA-256 of Type, Version, Source
// and Target, excluding Revision and UpdatedAt. Map insertion order does not
// affect the digest. It does not validate state or change UpdatedAt.
// Call it on non-nil, privately owned state before exposing it to a StateStore.
func (state *State) UpdateRevision() error {
	data, err := json.Marshal(struct {
		Type    string            `json:"type"`
		Version int               `json:"version"`
		Source  map[string]*Entry `json:"source"`
		Target  map[string]*Entry `json:"target"`
	}{Type: state.Type, Version: state.Version, Source: state.Source, Target: state.Target})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	state.Revision = hex.EncodeToString(hash[:])
	return nil
}

// save publishes confirmed content. The caller has already checked for changes.
func (state *State) save(ctx context.Context, store StateStore) error {
	if err := state.UpdateRevision(); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()
	if ctx.Err() != nil {
		// Give already confirmed operations a bounded chance to reach storage.
		// Normal saves keep the caller's cancellation and deadline.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	return store.Save(ctx, state)
}
