package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	stdpath "path"
	"sort"
	"strings"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/ioutil"
)

// FileInfo is a read-only file or directory snapshot supplied to callbacks.
// Path is absolute within the supplied FS. Accessors perform no filesystem I/O.
// Copy and mkdir callbacks describe the source; removal callbacks describe the
// entry being removed. The snapshot does not change after execution or retries.
type FileInfo interface {
	Path() string
	Size() int64
	Hash() string
	IsDir() bool
	ModTime() time.Time
}

type fileInfo struct {
	path  string
	entry Entry
}

func (info *fileInfo) Path() string {
	return info.path
}

func (info *fileInfo) Size() int64 {
	return info.entry.Size
}

func (info *fileInfo) Hash() string {
	return info.entry.Hash
}

func (info *fileInfo) IsDir() bool {
	return info.entry.IsDir
}

func (info *fileInfo) ModTime() time.Time {
	return info.entry.ModTime
}

type syncFS struct {
	cloudfs.FS
	option Option
}

func (fsys *syncFS) scan(ctx context.Context) (*syncTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	const root = "/"

	entries := make(map[string]*Entry)
	skipped := make(map[string]bool)
	if err := cloudfs.WalkDir(ctx, fsys, root, func(path string, info cloudfs.FileInfo, walkErr error) error {
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
		if !fs.ValidPath(strings.TrimPrefix(path, "/")) {
			return fmt.Errorf("sync: invalid path %q", path)
		}
		if entries[path] != nil || skipped[path] {
			return fmt.Errorf("sync: duplicate path %s", path)
		}
		entry := &Entry{IsDir: info.IsDir()}
		if !entry.IsDir {
			entry.Size, entry.ModTime = info.Size(), info.ModTime().UTC()
		}
		if fsys.option.Skipper != nil {
			skip := fsys.option.Skipper(entry.FileInfo(path))
			if err := ctx.Err(); err != nil {
				return err
			}
			if skip {
				skipped[path] = true
				if info.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		entries[path] = entry
		return nil
	}); err != nil {
		return nil, err
	}
	tree := newSyncTree(entries)
	tree.skipped = skipped
	return tree, nil
}

// readHash enriches one entry on demand. Cached entries are new snapshots;
// neither the scanned entries nor the loaded historical state are modified.
func (fsys *syncFS) readHash(ctx context.Context, tree *syncTree, path string, prev *Entry) (*Entry, error) {
	entry := tree.get(path)
	if entry == nil || entry.IsDir || entry.Hash != "" || !fsys.option.Checksum && !fsys.option.HashOnChange {
		return entry, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next := *entry
	if fsys.option.HashOnChange && !fsys.option.Checksum && prev != nil && !prev.IsDir &&
		prev.Hash != "" && entry.Size >= 0 && entry.Size == prev.Size &&
		!entry.ModTime.IsZero() && entry.ModTime.Equal(prev.ModTime) {
		next.Hash = prev.Hash
	} else {
		reader, err := fsys.Open(ctx, path)
		if err != nil {
			return nil, err
		}
		hasher := sha256.New()
		n, readErr := ioutil.CopyWithContext(ctx, hasher, reader)
		if err := errors.Join(readErr, reader.Close()); err != nil {
			return nil, err
		}
		if err := fsys.checkFileSnapshot(ctx, path, entry); err != nil {
			return nil, err
		}
		if entry.Size >= 0 && n != entry.Size {
			return nil, ErrSourceChanged
		}
		next.Hash = hex.EncodeToString(hasher.Sum(nil))
	}
	if tree.hashes == nil {
		tree.hashes = make(map[string]*Entry)
	}
	tree.hashes[path] = &next
	return &next, nil
}

// checkFileSnapshot compares metadata only; hashes are checked on existing reads.
func (fsys *syncFS) checkFileSnapshot(ctx context.Context, path string, entry *Entry) error {
	info, err := fsys.Stat(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.Join(ErrSourceChanged, err)
		}
		return err
	}
	if info.IsDir() ||
		entry.Size >= 0 && info.Size() >= 0 && entry.Size != info.Size() ||
		!entry.ModTime.Equal(info.ModTime()) {
		return ErrSourceChanged
	}
	return nil
}

// syncTree retains immutable scan entries and sorted children. Planning adds
// separately allocated hash snapshots; execution only reads the resulting view.
type syncTree struct {
	entries  map[string]*Entry
	hashes   map[string]*Entry // Entries enriched on demand during planning.
	children map[string][]string
	skipped  map[string]bool // Entries excluded during scanning.
}

// newSyncTree takes ownership of entries; neither the map nor its Entry objects
// may be modified afterward.
func newSyncTree(entries map[string]*Entry) *syncTree {
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

func (tree *syncTree) get(path string) *Entry {
	if entry := tree.hashes[path]; entry != nil {
		return entry
	}
	return tree.entries[path]
}

// walk visits included entries depth first, sorting children within each directory.
// Paths excluded during scanning are not passed to fn.
// SkipDir skips directory descendants; on a file it skips remaining siblings.
// SkipAll stops traversal successfully. Other errors are returned to the caller.
func (tree *syncTree) walk(fn func(string, *Entry) error) error {
	err := tree.walkPaths(tree.children["/"], fn)
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

// walkSubtree visits path and its descendants; a missing path visits nothing.
func (tree *syncTree) walkSubtree(path string, fn func(string, *Entry) error) error {
	if tree.get(path) == nil {
		return nil
	}
	err := tree.walkPaths([]string{path}, fn)
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

func (tree *syncTree) walkPaths(paths []string, fn func(string, *Entry) error) error {
	for _, path := range paths {
		entry := tree.get(path)
		if err := fn(path, entry); err != nil {
			if err == fs.SkipDir && entry.IsDir {
				continue
			}
			return err
		}
		if entry.IsDir {
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
