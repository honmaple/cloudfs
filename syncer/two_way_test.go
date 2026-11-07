package syncer

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/honmaple/cloudfs/driver/local"
)

func testScannedTree(current, previous map[string]*Record) *syncTree {
	tree := stateTree(current)
	for path, scanned := range tree.entries {
		scanned.record = previous[path]
	}
	return tree
}

func TestTwoWayTasks(t *testing.T) {
	old, changed := &Record{Size: 1}, &Record{Size: 2}
	directory := &Record{IsDir: true}
	for _, test := range []struct {
		name                         string
		source, target               map[string]*Record
		sourceState, targetState     map[string]*Record
		sourceSkipped, targetSkipped map[string]bool
		handler                      ConflictHandler
		want                         []string
		conflict                     bool
	}{
		{
			name:   "source addition",
			source: map[string]*Record{"/file": changed},
			want:   []string{"copy:false:/file"},
		},
		{
			name:   "target addition",
			target: map[string]*Record{"/file": changed},
			want:   []string{"copy:true:/file"},
		},
		{
			name:        "source edit",
			source:      map[string]*Record{"/file": changed},
			target:      map[string]*Record{"/file": old},
			sourceState: map[string]*Record{"/file": old},
			targetState: map[string]*Record{"/file": old},
			want:        []string{"copy:false:/file"},
		},
		{
			name:        "target edit",
			source:      map[string]*Record{"/file": old},
			target:      map[string]*Record{"/file": changed},
			sourceState: map[string]*Record{"/file": old},
			targetState: map[string]*Record{"/file": old},
			want:        []string{"copy:true:/file"},
		},
		{
			name:        "target deletion",
			source:      map[string]*Record{"/dir": directory, "/dir/file": old},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			want:        []string{"rmdir:true:/dir"},
		},
		{
			name:        "target deletion conflicts with removed source child",
			source:      map[string]*Record{"/dir": directory},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			conflict:    true,
		},
		{
			name:        "restore changed directory after checking children",
			source:      map[string]*Record{"/dir": directory, "/dir/file": changed},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			handler:     PreferSrc(),
			want:        []string{"mkdir:false:/dir", "copy:false:/dir/file"},
		},
		{
			name:        "prefer deletion after source child changed",
			source:      map[string]*Record{"/dir": directory, "/dir/file": changed},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			handler:     PreferDst(),
			want:        []string{"rmdir:true:/dir"},
		},
		{
			name:        "skip directory deletion conflict",
			source:      map[string]*Record{"/dir": directory, "/dir/file": changed},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			handler:     Skip(),
		},
		{
			name:        "target deletion conflicts with source edit",
			source:      map[string]*Record{"/file": changed},
			sourceState: map[string]*Record{"/file": old},
			targetState: map[string]*Record{"/file": old},
			conflict:    true,
		},
		{
			name:        "restore edited source after target deletion",
			source:      map[string]*Record{"/file": changed},
			sourceState: map[string]*Record{"/file": old},
			targetState: map[string]*Record{"/file": old},
			handler:     PreferSrc(),
			want:        []string{"copy:false:/file"},
		},
		{
			name:    "source directory replaces target file",
			source:  map[string]*Record{"/dir": directory, "/dir/file": changed},
			target:  map[string]*Record{"/dir": old},
			handler: PreferSrc(),
			want:    []string{"remove:false:/dir", "mkdir:false:/dir", "copy:false:/dir/file"},
		},
		{
			name:    "target directory replaces source file",
			source:  map[string]*Record{"/dir": old},
			target:  map[string]*Record{"/dir": directory, "/dir/file": changed},
			handler: PreferDst(),
			want:    []string{"remove:true:/dir", "mkdir:true:/dir", "copy:true:/dir/file"},
		},
		{
			name:          "target exclusion prunes source subtree",
			source:        map[string]*Record{"/dir": directory, "/dir/file": changed},
			targetSkipped: map[string]bool{"/dir": true},
		},
		{
			name:          "source exclusion blocks directory deletion",
			source:        map[string]*Record{"/dir": directory},
			sourceState:   map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState:   map[string]*Record{"/dir": directory, "/dir/file": old},
			sourceSkipped: map[string]bool{"/dir/file": true},
		},
		{
			name:    "skip conflict prunes source subtree",
			source:  map[string]*Record{"/dir": directory, "/dir/file": changed},
			target:  map[string]*Record{"/dir": old},
			handler: Skip(),
		},
		{
			name:   "new target directory",
			target: map[string]*Record{"/dir": directory, "/dir/file": changed},
			want:   []string{"mkdir:true:/dir", "copy:true:/dir/file"},
		},
		{
			name:   "target-only child of shared directory",
			source: map[string]*Record{"/dir": directory},
			target: map[string]*Record{"/dir": directory, "/dir/file": changed},
			want:   []string{"copy:true:/dir/file"},
		},
		{
			name:        "source deletion",
			target:      map[string]*Record{"/dir": directory, "/dir/file": old},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			want:        []string{"rmdir:false:/dir"},
		},
		{
			name:        "source deletion conflicts with target edit",
			target:      map[string]*Record{"/dir": directory, "/dir/file": changed},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			conflict:    true,
		},
		{
			name:        "restore target after source deletion",
			target:      map[string]*Record{"/dir": directory, "/dir/file": changed},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			handler:     PreferDst(),
			want:        []string{"mkdir:true:/dir", "copy:true:/dir/file"},
		},
		{
			name:        "target removed child conflicts with source deletion",
			target:      map[string]*Record{"/dir": directory},
			sourceState: map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState: map[string]*Record{"/dir": directory, "/dir/file": old},
			conflict:    true,
		},
		{
			name:    "do not revisit children of replaced target directory",
			source:  map[string]*Record{"/dir": changed},
			target:  map[string]*Record{"/dir": directory, "/dir/file": old},
			handler: PreferSrc(),
			want:    []string{"rmdir:false:/dir", "copy:false:/dir"},
		},
		{
			name:          "source exclusion protects target subtree",
			target:        map[string]*Record{"/dir": directory, "/dir/file": old},
			sourceSkipped: map[string]bool{"/dir": true},
		},
		{
			name:          "target exclusion blocks source deletion",
			target:        map[string]*Record{"/dir": directory},
			sourceState:   map[string]*Record{"/dir": directory, "/dir/file": old},
			targetState:   map[string]*Record{"/dir": directory, "/dir/file": old},
			targetSkipped: map[string]bool{"/dir/file": true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := test.handler
			if handler == nil {
				handler = Abort()
			}
			worker := &syncer{
				srcFS:            &syncFS{},
				dstFS:            &syncFS{},
				srcTree:          testScannedTree(test.source, test.sourceState),
				dstTree:          testScannedTree(test.target, test.targetState),
				state:            &State{Source: test.sourceState, Target: test.targetState},
				option:           Option{Mode: ModeTwoWay, OnConflict: handler},
				skippedConflicts: make(map[string]bool),
			}
			worker.srcTree.skipped, worker.dstTree.skipped = test.sourceSkipped, test.targetSkipped
			tasks, err := worker.twoWayTasks(context.Background())
			if errors.Is(err, ErrSyncConflict) != test.conflict || err != nil && !test.conflict {
				t.Fatalf("unexpected planning error: %v", err)
			}
			var got []string
			for _, task := range tasks {
				got = append(got, fmt.Sprintf("%s:%t:%s", task.op, task.reverse, task.path))
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("tasks=%v want=%v", got, test.want)
			}
		})
	}
}

func TestTwoWaySourceChecksumWithoutHistory(t *testing.T) {
	ctx := context.Background()
	var filesystems [2]*syncFS
	var trees [2]*syncTree
	var observed [2]*pipelineFS
	stamp := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for side, content := range []string{"aaaa", "bbbb"} {
		root := t.TempDir()
		filename := filepath.Join(root, "file")
		if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filename, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		fsys, err := local.New(&local.Option{Path: root})
		if err != nil {
			t.Fatal(err)
		}
		defer fsys.Close()
		observed[side] = &pipelineFS{FS: fsys, t: t, planning: true}
		filesystems[side] = &syncFS{algorithm: crypto.SHA256, FS: observed[side]}
		trees[side], err = filesystems[side].scan(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	worker := &syncer{
		srcFS:   filesystems[0],
		dstFS:   filesystems[1],
		srcTree: trees[0],
		dstTree: trees[1],
		state:   &State{},
		option: Option{
			Mode:       ModeTwoWay,
			Checksum:   true,
			OnConflict: Abort(),
		},
	}
	if _, err := worker.twoWayTasks(ctx); !errors.Is(err, ErrSyncConflict) {
		t.Fatalf("same metadata hid different contents: %v", err)
	}
	for _, fsys := range observed {
		if fsys.reads != 1 || fsys.writes != 0 {
			t.Fatalf("reads=%d writes=%d", fsys.reads, fsys.writes)
		}
	}
	// Resolve a deletion against a different historical version. The resulting
	// copy must retain the hash read by the local conflict handler.
	worker.state.Source = map[string]*Record{"/file": trees[1].get("/file").toRecord()}
	worker.state.Target = map[string]*Record{"/file": trees[1].get("/file").toRecord()}
	sourceTree, err := worker.srcFS.scan(ctx, worker.state.Source)
	if err != nil {
		t.Fatal(err)
	}
	worker.srcTree = sourceTree
	worker.dstTree = stateTree(nil)
	worker.option.OnConflict = PreferSrc()
	tasks, err := worker.twoWayTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].record.Hash != worker.srcTree.get("/file").hash {
		t.Fatalf("deletion conflict lost the compared source snapshot: tasks=%+v err=%v", tasks, err)
	}
}
