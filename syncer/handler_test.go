package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/syncer"
)

func TestBuiltInConflictHandlers(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler syncer.ConflictHandler
		want    syncer.ConflictPolicy
	}{
		{
			name:    "abort",
			handler: syncer.Abort(),
			want:    syncer.ConflictAbort,
		},
		{
			name:    "skip",
			handler: syncer.Skip(),
			want:    syncer.ConflictSkip,
		},
		{
			name:    "source",
			handler: syncer.PreferSrc(),
			want:    syncer.ConflictPreferSrc,
		},
		{
			name:    "target",
			handler: syncer.PreferDst(),
			want:    syncer.ConflictPreferDst,
		},
		{
			name:    "both",
			handler: syncer.KeepBoth(),
			want:    syncer.ConflictKeepBoth,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if policy, err := test.handler(context.Background(), syncer.Conflict{}); err != nil || policy != test.want {
				t.Fatalf("policy=%d err=%v", policy, err)
			}
		})
	}
}

func TestPreferNewerDecisions(t *testing.T) {
	earlier := (&syncer.Entry{
		ModTime: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}).FileInfo("/file")
	later := (&syncer.Entry{
		ModTime: time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
	}).FileInfo("/file")
	unknown := (&syncer.Entry{}).FileInfo("/file")
	directory := (&syncer.Entry{
		IsDir: true,
	}).FileInfo("/file")
	for _, test := range []struct {
		name           string
		source, target syncer.FileInfo
		want           syncer.ConflictPolicy
	}{
		{
			name: "both absent",
			want: syncer.ConflictAbort,
		},
		{
			name:   "source survives",
			source: earlier,
			want:   syncer.ConflictPreferSrc,
		},
		{
			name:   "target directory survives",
			target: directory,
			want:   syncer.ConflictPreferDst,
		},
		{
			name:   "source newer",
			source: later,
			target: earlier,
			want:   syncer.ConflictPreferSrc,
		},
		{
			name:   "target newer",
			source: earlier,
			target: later,
			want:   syncer.ConflictPreferDst,
		},
		{
			name:   "equal known times",
			source: earlier,
			target: earlier,
			want:   syncer.ConflictPreferSrc,
		},
		{
			name:   "source time unknown",
			source: unknown,
			target: later,
			want:   syncer.ConflictAbort,
		},
		{
			name:   "target time unknown",
			source: later,
			target: unknown,
			want:   syncer.ConflictAbort,
		},
		{
			name:   "source is directory",
			source: directory,
			target: later,
			want:   syncer.ConflictAbort,
		},
		{
			name:   "target is directory",
			source: later,
			target: directory,
			want:   syncer.ConflictAbort,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := syncer.PreferNewer()(context.Background(), syncer.Conflict{
				Source: test.source,
				Target: test.target,
			})
			if err != nil || policy != test.want {
				t.Fatalf("policy=%d err=%v", policy, err)
			}
		})
	}
}

func TestPreferNewerSynchronization(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		for _, newerSource := range []bool{false, true} {
			t.Run(fmt.Sprintf("bisync=%t/source-newer=%t", bisync, newerSource), func(t *testing.T) {
				src, dst, srcDir, dstDir := syncPair(t)
				putSyncFile(t, srcDir, "file", "source version")
				putSyncFile(t, dstDir, "file", "target version with more bytes")
				for side, root := range []string{srcDir, dstDir} {
					year := 2020
					if (side == 0) == newerSource {
						year++
					}
					stamp := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
					if err := os.Chtimes(filepath.Join(root, "file"), stamp, stamp); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if bisync {
					err = syncer.Sync(context.Background(), src, dst, syncer.Option{
						Mode:       syncer.ModeTwoWay,
						StateStore: &singleSyncStore{},
						OnConflict: syncer.PreferNewer(),
					})
				} else {
					err = syncer.Sync(context.Background(), src, dst, syncer.Option{
						OnConflict: syncer.PreferNewer(),
					})
				}
				if err != nil {
					t.Fatal(err)
				}
				winner := "target version with more bytes"
				if newerSource {
					winner = "source version"
				}
				wantSyncFile(t, dstDir, "file", winner)
				if bisync {
					wantSyncFile(t, srcDir, "file", winner)
				} else {
					wantSyncFile(t, srcDir, "file", "source version")
				}
			})
		}
	}
}

func TestNilConflictHandlerDefaultsToAbort(t *testing.T) {
	for _, bisync := range []bool{false, true} {
		t.Run(fmt.Sprint(bisync), func(t *testing.T) {
			src, dst, srcDir, dstDir := syncPair(t)
			putSyncFile(t, srcDir, "a-first", "not copied")
			putSyncFile(t, srcDir, "z-conflict", "source version")
			putSyncFile(t, dstDir, "z-conflict", "target version with more bytes")
			var err error
			if bisync {
				err = syncer.Sync(context.Background(), src, dst, syncer.Option{
					Mode:       syncer.ModeTwoWay,
					StateStore: &singleSyncStore{},
				})
			} else {
				err = syncer.Sync(context.Background(), src, dst)
			}
			if !errors.Is(err, syncer.ErrSyncConflict) {
				t.Fatalf("got %v", err)
			}
			wantSyncAbsent(t, dstDir, "a-first")
			wantSyncFile(t, dstDir, "z-conflict", "target version with more bytes")
		})
	}
}

func TestPreferNewerAbortsTypeConflictBeforeWrites(t *testing.T) {
	src, dst, srcDir, dstDir := syncPair(t)
	putSyncFile(t, srcDir, "a-first", "not copied")
	putSyncFile(t, srcDir, "z-conflict", "source file")
	putSyncFile(t, dstDir, "z-conflict/child", "target directory")
	err := syncer.Sync(context.Background(), src, dst, syncer.Option{
		Mode:       syncer.ModeTwoWay,
		StateStore: &singleSyncStore{},
		OnConflict: syncer.PreferNewer(),
	})
	if !errors.Is(err, syncer.ErrSyncConflict) {
		t.Fatalf("got %v", err)
	}
	wantSyncAbsent(t, dstDir, "a-first")
	wantSyncFile(t, srcDir, "z-conflict", "source file")
	wantSyncFile(t, dstDir, "z-conflict/child", "target directory")
}

func fixedConflict(policy syncer.ConflictPolicy) syncer.ConflictHandler {
	return func(context.Context, syncer.Conflict) (syncer.ConflictPolicy, error) {
		return policy, nil
	}
}
