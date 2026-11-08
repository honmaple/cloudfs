package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/syncer"
)

// Each round changes one side's tree and adds an independent file on the other
// side. The expected result comes from those edits, not from the sync planner.
// Fixed seeds and timestamps make a failing sequence reproducible.
func TestBiSyncRandomChanges(t *testing.T) {
	for _, checksum := range []bool{false, true} {
		for _, faults := range []bool{false, true} {
			for seed := int64(1); seed <= 12; seed++ {
				t.Run(fmt.Sprintf("checksum=%t/faults=%t/seed=%d", checksum, faults, seed), func(t *testing.T) {
					testBiSyncRandomChanges(t, seed, checksum, faults)
				})
			}
		}
	}
}

func TestSyncRandomMirror(t *testing.T) {
	for _, checksum := range []bool{false, true} {
		for seed := int64(1); seed <= 12; seed++ {
			t.Run(fmt.Sprintf("checksum=%t/seed=%d", checksum, seed), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				random := rand.New(rand.NewSource(seed))
				store := &singleSyncStore{}
				option := syncer.Option{
					Checksum: checksum,
					Delete:   true,

					StateStore: store,
					OnConflict: syncer.PreferSrc(),
				}
				for round := 0; round < 8; round++ {
					for _, root := range []string{srcDir, dstDir} {
						for edit, count := 0, 1+random.Intn(4); edit < count; edit++ {
							mutateSyncTree(t, random, root, seed, round, edit)
						}
					}
					want := biSyncSubtree(t, srcDir, ".")
					option.OnEvent = nil
					if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
						t.Fatalf("round %d: %v", round, err)
					}
					for _, root := range []string{srcDir, dstDir} {
						if got := biSyncSubtree(t, root, "."); !reflect.DeepEqual(got, want) {
							t.Fatalf("round %d: got %v, want %v", round, got, want)
						}
					}
					saves := store.saves
					option.OnEvent = taskEvents(func(event syncer.Event) {
						t.Fatalf("round %d: non-idempotent event: %+v", round, event)
					})
					if err := syncer.Sync(context.Background(), src, dst, option); err != nil {
						t.Fatalf("round %d: repeated run: %v", round, err)
					}
					if store.saves != saves {
						t.Fatalf("round %d: repeated run saved state", round)
					}
				}
			})
		}
	}
}

func testBiSyncRandomChanges(t *testing.T, seed int64, checksum, faults bool) {
	t.Helper()
	src, dst, srcDir, dstDir := syncPair(t)
	mutations := rand.New(rand.NewSource(seed))
	injection := &randomSyncFault{
		random: rand.New(rand.NewSource(seed + 1000)),
	}
	left := &randomFaultFS{
		FS:    src,
		fault: injection,
	}
	right := &randomFaultFS{
		FS:    dst,
		fault: injection,
	}
	store := &singleSyncStore{}
	ctx := context.Background()
	for round := 0; round < 8; round++ {
		root, other := srcDir, dstDir
		policy := syncer.ConflictPreferSrc
		if mutations.Intn(2) == 1 {
			root, other = other, root
			policy = syncer.ConflictPreferDst
		}
		for edit, count := 0, 1+mutations.Intn(4); edit < count; edit++ {
			mutateSyncTree(t, mutations, root, seed, round, edit)
		}
		want := biSyncSubtree(t, root, ".")
		independent := fmt.Sprintf("independent-%d", round)
		content := fmt.Sprintf("other side round %d", round)
		putSyncFile(t, other, independent, content)
		want[independent] = content
		option := syncer.Option{
			Mode:       syncer.ModeTwoWay,
			Checksum:   checksum,
			StateStore: store,
			// A partial write is an unconfirmed conflict on the next run.
			// Resolve it using the known author of this round's edits.
			OnConflict: func(_ context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
				info := conflict.Source
				if info == nil {
					info = conflict.Target
				}
				if info.Path() == independent {
					if policy == syncer.ConflictPreferSrc {
						return syncer.ConflictPreferDst, nil
					}
					return syncer.ConflictPreferSrc, nil
				}
				return policy, nil
			},
		}
		if faults {
			// Exercise both stopping and continuing after a file failure.
			if round%2 == 1 {
				option.OnError = func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
					if !errors.Is(failure.Err, errSyncTest) {
						t.Fatalf("round %d: unexpected operation error: %v", round, failure.Err)
					}
					return syncer.ErrorSkip, nil
				}
			}
			injection.enabled = true
			err := syncer.Sync(ctx, left, right, option)
			injection.enabled = false
			if err != nil && !errors.Is(err, errSyncTest) {
				t.Fatalf("round %d: faulty run: %v", round, err)
			}
			option.OnError = nil
		}
		if err := syncer.Sync(ctx, left, right, option); err != nil {
			t.Fatalf("round %d: healthy run: %v", round, err)
		}
		for _, dir := range []string{srcDir, dstDir} {
			if got := biSyncSubtree(t, dir, "."); !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d: %s\ngot  %v\nwant %v", round, dir, got, want)
			}
		}
		// A repeated run must neither write nor refresh saved state.
		saves := store.saves
		option.OnConflict = nil
		option.OnEvent = taskEvents(func(event syncer.Event) {
			t.Fatalf("round %d: non-idempotent event: %+v", round, event)
		})
		if err := syncer.Sync(ctx, left, right, option); err != nil {
			t.Fatalf("round %d: repeated run: %v", round, err)
		}
		if store.saves != saves {
			t.Fatalf("round %d: repeated run saved state", round)
		}
	}
	if faults && injection.failures == 0 {
		t.Fatal("no faults were exercised")
	}
}

func mutateSyncTree(t *testing.T, random *rand.Rand, root string, seed int64, round, edit int) {
	t.Helper()
	paths := []string{"a", "b", "dir", "dir/child", "dir/nested/file", "empty"}
	path := paths[random.Intn(len(paths))]
	deleting := random.Intn(3) == 0
	// Remove file ancestors so edits can also exercise file/directory replacements.
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		parent := filepath.Join(parts[:i]...)
		info, err := os.Stat(filepath.Join(root, parent))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if deleting && (err != nil || !info.IsDir()) {
			return
		}
		if err == nil && !info.IsDir() {
			removeSyncPath(t, root, parent)
		}
	}
	removeSyncPath(t, root, path)
	if deleting {
		return
	}
	if random.Intn(3) == 0 {
		if err := os.MkdirAll(filepath.Join(root, path), 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	putSyncFile(t, root, path, fmt.Sprintf("seed=%d round=%d edit=%d", seed, round, edit))
	stamp := time.Date(2020, 1, 1, 0, round, edit, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, path), stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

type randomSyncFault struct {
	random   *rand.Rand
	enabled  bool
	failures int
}

func (fault *randomSyncFault) fail() bool {
	if !fault.enabled || fault.random.Intn(4) != 0 {
		return false
	}
	fault.failures++
	return true
}

type randomFaultFS struct {
	cloudfs.FS
	fault *randomSyncFault
}

func (fsys *randomFaultFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	if fsys.fault.fail() {
		return nil, errSyncTest
	}
	writer, err := fsys.FS.Create(ctx, path)
	if err != nil {
		return nil, err
	}
	return &randomFaultWriter{
		FileWriter: writer,
		fault:      fsys.fault,
	}, nil
}

func (fsys *randomFaultFS) MakeDir(ctx context.Context, path string) error {
	if fsys.fault.fail() {
		return errSyncTest
	}
	return fsys.FS.MakeDir(ctx, path)
}

func (fsys *randomFaultFS) Remove(ctx context.Context, path string) error {
	if fsys.fault.fail() {
		return errSyncTest
	}
	return fsys.FS.Remove(ctx, path)
}

type randomFaultWriter struct {
	cloudfs.FileWriter
	fault *randomSyncFault
}

func (writer *randomFaultWriter) Write(data []byte) (int, error) {
	if writer.fault.fail() {
		n, err := writer.FileWriter.Write(data[:len(data)/2])
		return n, errors.Join(err, errSyncTest)
	}
	return writer.FileWriter.Write(data)
}

func (writer *randomFaultWriter) Close() error {
	err := writer.FileWriter.Close()
	if writer.fault.fail() {
		return errors.Join(err, errSyncTest)
	}
	return err
}
