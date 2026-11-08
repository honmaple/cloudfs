package syncer

import (
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	stdpath "path"
	"strings"
	"time"
)

// Record describes a file or directory in a synchronization snapshot or State.
// Paths are the keys of State.Source and State.Target.
type Record struct {
	IsDir   bool      `json:"is_dir,omitempty"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
	Hash    string    `json:"hash,omitempty"`
}

// Valid reports whether the record has valid synchronization metadata.
// A nil record is invalid. Directories must have zero size, time and hash;
// nonempty file hashes must be hex digests of the supplied algorithm. It performs no I/O.
func (record *Record) Valid(algorithm crypto.Hash) bool {
	if record == nil {
		return false
	}
	if record.IsDir {
		return record.Size == 0 && record.ModTime.IsZero() && record.Hash == ""
	}
	if record.Hash == "" {
		return true
	}
	hash, err := hex.DecodeString(record.Hash)
	return err == nil && len(hash) == algorithm.Size()
}

// Equal compares synchronization information without reading file contents.
// Two nil records are equal. Directories compare only by type, not their subtrees.
// Files compare hashes when both are nonempty, otherwise size and modification time.
func (record *Record) Equal(other *Record) bool {
	if record == nil || other == nil {
		return record == other
	}
	if record.IsDir != other.IsDir {
		return false
	}
	if record.IsDir {
		return true
	}
	if record.Hash != "" && other.Hash != "" {
		return record.Hash == other.Hash
	}
	return record.Size == other.Size && record.ModTime.Equal(other.ModTime)
}

// FileInfo adapts a record to a read-only view at the given relative path.
// A nil record returns nil. The returned snapshot is independent of later record changes.
func (record *Record) FileInfo(path string) FileInfo {
	snapshot := record.entry(path)
	if snapshot == nil {
		return nil
	}
	return snapshot
}

// entry adapts historical metadata for comparison without attaching a filesystem.
func (record *Record) entry(path string) *entry {
	if record == nil {
		return nil
	}
	return &entry{path: path, size: record.Size, modTime: record.ModTime, isDir: record.IsDir, hash: record.Hash}
}

// withHash returns a confirmed source snapshot without modifying the original record.
func (record *Record) withHash(hash string) *Record {
	if record == nil || record.IsDir || record.Hash == hash {
		return record
	}
	confirmed := *record
	confirmed.Hash = hash
	return &confirmed
}

// State contains the confirmed per-side file metadata used as a historical
// comparison baseline. It may include partial successes, not execution progress.
// Its exported fields support serialization, for example with encoding/json.
// Treat its contents as immutable once supplied to or received from Sync.
type State struct {
	Source        map[string]*Record `json:"source"`
	Target        map[string]*Record `json:"target"`
	Version       int                `json:"version"`        // Protocol version; currently 1.
	Revision      string             `json:"revision"`       // Identifies the state content.
	UpdatedAt     time.Time          `json:"updated_at"`     // UTC time when changed state was prepared for saving.
	HashAlgorithm crypto.Hash        `json:"hash_algorithm"` // Algorithm shared by all record hashes.
}

// apply records confirmed task results in execution stage order.
func (state *State) apply(results []taskResult) {
	for _, result := range results {
		task := result.task
		if task == nil {
			continue
		}
		src, dst := state.Source, state.Target
		if task.reverse {
			src, dst = state.Target, state.Source
		}
		switch task.op {
		case OpRename:
			dst[task.newPath] = result.record
			delete(dst, task.path)
		case OpCopy, OpMkdir:
			src[task.path] = task.record.withHash(result.record.Hash)
			dst[task.path] = result.record
		case OpRemove, OpRmdir:
			delete(dst, task.path)
			if !task.replace {
				delete(src, task.path)
			}
			if task.op == OpRmdir {
				// Neither side can retain confirmed descendants of the removed directory.
				prefix := task.path + "/"
				for _, records := range []map[string]*Record{src, dst} {
					for path := range records {
						if strings.HasPrefix(path, prefix) {
							delete(records, path)
						}
					}
				}
			}
		}
	}
}

// Valid reports whether state has a supported protocol version, a nonempty
// revision and valid record maps, paths and directory parents. Nil is invalid.
// It does not recompute Revision or read the filesystems.
func (state *State) Valid() bool {
	if state == nil || state.Version != 1 || state.Revision == "" || !state.HashAlgorithm.Available() {
		return false
	}
	validRecords := func(records map[string]*Record) bool {
		if records == nil {
			return false
		}
		for path, record := range records {
			if record == nil || !fs.ValidPath(path) || path == "." || !record.Valid(state.HashAlgorithm) {
				return false
			}
			if parentPath := stdpath.Dir(path); parentPath != "." {
				if parent := records[parentPath]; parent == nil || !parent.IsDir {
					return false
				}
			}
		}
		return true
	}
	return validRecords(state.Source) && validRecords(state.Target)
}

// UpdateRevision replaces Revision with the SHA-256 of HashAlgorithm, Version, Source
// and Target, excluding Revision and UpdatedAt. Map insertion order does not
// affect the digest. It does not validate state or change UpdatedAt.
// Call it on non-nil, privately owned state before exposing it to a StateStore.
func (state *State) UpdateRevision() error {
	data, err := json.Marshal(struct {
		HashAlgorithm crypto.Hash        `json:"hash_algorithm"` // Algorithm shared by all record hashes.
		Version       int                `json:"version"`
		Source        map[string]*Record `json:"source"`
		Target        map[string]*Record `json:"target"`
	}{HashAlgorithm: state.HashAlgorithm, Version: state.Version, Source: state.Source, Target: state.Target})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	state.Revision = hex.EncodeToString(hash[:])
	return nil
}

func stateTree(records map[string]*Record) *syncTree {
	entries := make(map[string]*entry, len(records))
	for path, record := range records {
		entries[path] = record.entry(path)
	}
	return newSyncTree(entries)
}
