package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

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

// FileStore persists synchronization state in a local JSON file.
// Use NewFileStore to bind the file to an ordered pair of endpoints.
type (
	FileStore struct {
		filename string
		source   string
		target   string
	}
	stateDocument struct {
		Source string `json:"source"`
		Target string `json:"target"`
		State  *State `json:"metadata"`
	}
)

// Load reads the saved state. A missing file returns nil.
func (s *FileStore) Load(ctx context.Context) (*State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.filename)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var document stateDocument
	if json.Unmarshal(data, &document) != nil || document.State == nil {
		return nil, fmt.Errorf("%w: invalid state file", ErrSyncState)
	}
	if document.Source != s.source || document.Target != s.target {
		return nil, fmt.Errorf("%w: state file belongs to different endpoints or configuration", ErrSyncState)
	}
	return document.State, nil
}

// Save writes state to the local file, replacing the previous contents.
func (s *FileStore) Save(ctx context.Context, state *State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(stateDocument{
		Source: s.source,
		Target: s.target,
		State:  state,
	})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.filename), ".cloudfs-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(name, s.filename)
}

// NewFileStore binds filename to the caller's stable source and target
// identifiers. The file's parent directory must exist before saving.
func NewFileStore(filename, source, target string) *FileStore {
	return &FileStore{
		filename: filename,
		source:   source,
		target:   target,
	}
}
