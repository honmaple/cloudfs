package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver/local"
)

type pipelineFS struct {
	cloudfs.FS
	t        *testing.T
	planning bool
	reads    int
	writes   int
}

func (fsys *pipelineFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	if !fsys.planning {
		fsys.t.Fatal("scan opened file contents")
	}
	fsys.reads++
	return fsys.FS.Open(ctx, path)
}

func (fsys *pipelineFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	fsys.writes++
	return fsys.FS.Create(ctx, path)
}

func TestSyncPipelinePlansConcreteEndpoints(t *testing.T) {
	ctx := context.Background()
	var roots [2]string
	var filesystems [2]*syncFS
	var trees [2]*syncTree
	var observed [2]*pipelineFS
	for side := range roots {
		roots[side] = t.TempDir()
		fsys, err := local.New(&local.Option{Path: roots[side]})
		if err != nil {
			t.Fatal(err)
		}
		defer fsys.Close()
		observed[side] = &pipelineFS{FS: fsys, t: t}
		filesystems[side] = &syncFS{FS: observed[side], option: Option{Checksum: true}}
	}
	if err := os.WriteFile(filepath.Join(roots[0], "file"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	for side := range filesystems {
		var err error
		trees[side], err = filesystems[side].scan(ctx)
		if err != nil {
			t.Fatal(err)
		}
		observed[side].planning = true
	}
	worker := &syncer{
		srcFS:   filesystems[0],
		dstFS:   filesystems[1],
		srcTree: trees[0],
		dstTree: trees[1],
		state:   &State{Source: make(map[string]*Entry), Target: make(map[string]*Entry)},
	}
	tasks, err := worker.oneWayTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for side, fsys := range observed {
		if fsys.writes != 0 || fsys.reads != 0 {
			t.Fatal("new file planning performed I/O")
		}
		if scanned := trees[side].entries["/file"]; scanned != nil && (scanned.Hash != "" || trees[side].get("/file") != scanned) {
			t.Fatal("planning changed the scanned entry")
		}
	}
	if len(tasks) != 1 || tasks[0].op != OpCopy || tasks[0].path != "/file" {
		t.Fatal("unexpected copy plan")
	}
	exec := &executor{
		srcFS: filesystems[0],
		dstFS: filesystems[1],
		option: Option{
			Concurrency: 2,
		},
		state: &State{
			Source: make(map[string]*Entry),
			Target: make(map[string]*Entry),
		},
	}
	if err := exec.run(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	if observed[0].reads+observed[1].reads != 1 {
		t.Fatal("new file was read more than once for transfer")
	}
	for _, root := range roots {
		data, err := os.ReadFile(filepath.Join(root, "file"))
		if err != nil || string(data) != "source" {
			t.Fatalf("unexpected final content: %q, %v", data, err)
		}
	}
}

func TestScanExcludesFilteredPaths(t *testing.T) {
	ctx := context.Background()
	var filesystems [2]*syncFS
	var observed [2]*pipelineFS
	for side := range filesystems {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "excluded"), 0755); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"file", "excluded/child", "excluded-file"} {
			if err := os.WriteFile(filepath.Join(root, path), []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		fsys, err := local.New(&local.Option{Path: root})
		if err != nil {
			t.Fatal(err)
		}
		defer fsys.Close()
		observed[side] = &pipelineFS{FS: fsys, t: t}
		filesystems[side] = &syncFS{
			FS: observed[side],
			option: Option{Skipper: func(info FileInfo) bool {
				return info.Path() == "/excluded" || info.Path() == "/excluded-file"
			}},
		}
	}
	for side, fsys := range filesystems {
		tree, err := fsys.scan(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(tree.entries) != 1 || !tree.skipped["/excluded"] || !tree.skipped["/excluded-file"] || tree.get("/file").Hash != "" {
			t.Fatalf("unexpected scan result: %+v", tree)
		}
		if observed[side].writes != 0 || observed[side].reads != 0 {
			t.Fatal("bidirectional scan read contents or wrote files")
		}
	}
}
