package syncer

import (
	"context"
	"crypto"
	_ "crypto/md5"
	_ "crypto/sha1"
	_ "crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	stdpath "path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/ioutil"
)

// FileInfo is a read-only file or directory snapshot supplied to callbacks.
// Path is relative to the synchronization root. Accessors perform no filesystem I/O.
// Copy and mkdir callbacks describe the source; removal callbacks describe the
// entry being removed. The snapshot does not change after execution or retries.
type FileInfo interface {
	Path() string
	Size() int64
	Hash() string
	IsDir() bool
	ModTime() time.Time
}

// entry retains scanned metadata and loads its content hash on demand.
type entry struct {
	path    string
	size    int64
	modTime time.Time
	isDir   bool
	hash    string
	fsys    *syncFS
	record  *Record // Historical baseline for this side and path.
}

// toRecord captures only metadata and hashes already known; it never performs I/O.
func (e *entry) toRecord() *Record {
	if e == nil {
		return nil
	}
	return &Record{IsDir: e.isDir, Size: e.size, ModTime: e.modTime, Hash: e.hash}
}

// isChanged compares the entry with its historical record.
// A newly known hash cannot confirm unchanged content against a record without a hash.
func (e *entry) isChanged() bool {
	if e.record == nil {
		return true
	}
	return !e.Equal(e.record.entry(e.path)) || e.hash != "" && e.record.Hash == ""
}

func (e *entry) loadHash(ctx context.Context) error {
	if e.isDir || e.hash != "" || e.fsys == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if record := e.record; record != nil &&
		!record.IsDir && record.Hash != "" && e.size >= 0 && e.size == record.Size &&
		!e.modTime.IsZero() && e.modTime.Equal(record.ModTime) {
		e.hash = record.Hash
		return nil
	}
	hash, err := e.fsys.Hash(ctx, e.path, e.fsys.algorithm)
	if err != nil {
		return err
	}
	e.hash = hash
	return nil
}

func (e *entry) Path() string {
	return e.path
}

func (e *entry) Size() int64 {
	return e.size
}

func (e *entry) Hash() string {
	return e.hash
}

func (e *entry) ModTime() time.Time {
	return e.modTime
}

func (e *entry) IsDir() bool {
	return e.isDir
}

// FileInfo captures a read-only snapshot of the entry's known metadata.
func (e *entry) FileInfo() FileInfo {
	if e == nil {
		return nil
	}
	snapshot := *e
	snapshot.fsys, snapshot.record = nil, nil
	return &snapshot
}

// Equal compares known metadata and hashes without performing I/O.
func (e *entry) Equal(other *entry) bool {
	if e == nil || other == nil {
		return e == other
	}
	if e.isDir != other.isDir {
		return false
	}
	if e.isDir {
		return true
	}
	if e.hash != "" && other.hash != "" {
		return e.hash == other.hash
	}
	return e.size == other.size && e.modTime.Equal(other.modTime)
}

// syncFS adapts relative sync paths to driver paths rooted at /.
type syncFS struct {
	FS        cloudfs.FS
	option    Option
	algorithm crypto.Hash
}

func (fsys *syncFS) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	return fsys.FS.Stat(ctx, "/"+path)
}

func (fsys *syncFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	return fsys.FS.Open(ctx, "/"+path)
}

func (fsys *syncFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	return fsys.FS.Create(ctx, "/"+path)
}

func (fsys *syncFS) MakeDir(ctx context.Context, path string) error {
	return fsys.FS.MakeDir(ctx, "/"+path)
}

func (fsys *syncFS) Remove(ctx context.Context, path string) error {
	return fsys.FS.Remove(ctx, "/"+path)
}

func (fsys *syncFS) Rename(ctx context.Context, path, name string) error {
	return fsys.FS.Rename(ctx, "/"+path, name)
}

func (fsys *syncFS) scan(ctx context.Context, records map[string]*Record) (*syncTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	const root = "/"

	entries := make(map[string]*entry)
	skipped := make(map[string]bool)
	if err := cloudfs.WalkDir(ctx, fsys.FS, root, func(path string, info cloudfs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			if !info.IsDir() {
				return fmt.Errorf("sync: root %s is not a directory", root)
			}
			return nil
		}
		entryName := info.Name()
		if entryName == "" || entryName == "." || entryName == ".." || strings.Contains(entryName, "/") {
			return fmt.Errorf("sync: invalid entry name %q", entryName)
		}
		relative := strings.TrimPrefix(path, "/")
		if !fs.ValidPath(relative) {
			return fmt.Errorf("sync: invalid path %q", path)
		}
		if entries[relative] != nil || skipped[relative] {
			return fmt.Errorf("sync: duplicate path %s", relative)
		}
		e := &entry{path: relative, isDir: info.IsDir(), fsys: fsys, record: records[relative]}
		if !e.isDir {
			e.size, e.modTime = info.Size(), info.ModTime().UTC()
		}
		if fsys.option.Skipper != nil {
			skip := fsys.option.Skipper(e.FileInfo())
			if err := ctx.Err(); err != nil {
				return err
			}
			if skip {
				skipped[relative] = true
				if info.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		entries[relative] = e
		return nil
	}); err != nil {
		return nil, err
	}
	tree := newSyncTree(entries)
	tree.skipped = skipped
	return tree, nil
}

// checkSnapshot compares a file's metadata with its recorded snapshot.
func (fsys *syncFS) checkSnapshot(ctx context.Context, path string, record *Record) error {
	info, err := fsys.Stat(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.Join(ErrSourceChanged, err)
		}
		return err
	}
	if info.IsDir() ||
		record.Size >= 0 && info.Size() >= 0 && record.Size != info.Size() ||
		!record.ModTime.Equal(info.ModTime()) {
		return ErrSourceChanged
	}
	return nil
}

// Hash prefers a backend digest and falls back to reading the file.
func (fsys *syncFS) Hash(ctx context.Context, path string, algorithm crypto.Hash) (string, error) {
	if hasher, ok := fsys.FS.(cloudfs.Hasher); ok {
		hash, err := hasher.Hash(ctx, "/"+path, algorithm)
		if err != nil && !errors.Is(err, cloudfs.ErrNotSupport) {
			return "", err
		}
		if err == nil && hash != "" {
			return hash, nil
		}
	}
	if !algorithm.Available() {
		return "", cloudfs.ErrNotSupport
	}
	reader, err := fsys.Open(ctx, path)
	if err != nil {
		return "", err
	}
	hasher := algorithm.New()
	_, readErr := ioutil.CopyWithContext(ctx, hasher, reader)
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// Hashes includes backend algorithms and algorithms available for streamed hashing.
func (fsys *syncFS) Hashes() []crypto.Hash {
	var algorithms []crypto.Hash
	if hasher, ok := fsys.FS.(cloudfs.Hasher); ok {
		algorithms = slices.Clone(hasher.Hashes())
	}
	for _, algorithm := range []crypto.Hash{
		crypto.MD5, crypto.SHA1, crypto.SHA256, crypto.SHA384,
		crypto.SHA512, crypto.SHA512_224, crypto.SHA512_256,
	} {
		if !slices.Contains(algorithms, algorithm) {
			algorithms = append(algorithms, algorithm)
		}
	}
	return algorithms
}

// syncTree retains file information and sorted children for planning.
type syncTree struct {
	skipped  map[string]bool // Entries excluded during scanning.
	entries  map[string]*entry
	children map[string][]string
}

// newSyncTree takes ownership of entries. Hashes are populated during planning.
func newSyncTree(entries map[string]*entry) *syncTree {
	children := make(map[string][]string)
	for path := range entries {
		parent := stdpath.Dir(path)
		children[parent] = append(children[parent], path)
	}
	for _, paths := range children {
		sort.Strings(paths)
	}
	return &syncTree{entries: entries, children: children}
}

func (tree *syncTree) get(path string) *entry {
	return tree.entries[path]
}

// walk visits included entries depth first, sorting children within each directory.
// Paths excluded during scanning are not passed to fn.
// SkipDir skips directory descendants; on a file it skips remaining siblings.
// SkipAll stops traversal successfully. Other errors are returned to the caller.
func (tree *syncTree) walk(fn func(string, *entry) error) error {
	err := tree.walkPaths(tree.children["."], fn)
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

// walkSubtree visits path and its descendants; a missing path visits nothing.
func (tree *syncTree) walkSubtree(path string, fn func(string, *entry) error) error {
	if tree.get(path) == nil {
		return nil
	}
	err := tree.walkPaths([]string{path}, fn)
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

func (tree *syncTree) walkPaths(paths []string, fn func(string, *entry) error) error {
	for _, path := range paths {
		child := tree.get(path)
		if err := fn(path, child); err != nil {
			if err == fs.SkipDir && child.IsDir() {
				continue
			}
			return err
		}
		if child.IsDir() {
			if err := tree.walkPaths(tree.children[path], fn); err != nil && err != fs.SkipDir {
				return err
			}
		}
	}
	return nil
}

// isSkipped reports whether path is excluded, optionally including descendants.
func (tree *syncTree) isSkipped(path string, recursive bool) bool {
	if tree.skipped[path] {
		return true
	}
	if !recursive {
		return false
	}
	for skipped := range tree.skipped {
		if strings.HasPrefix(skipped, path+"/") {
			return true
		}
	}
	return false
}
