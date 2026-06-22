package middleware

import (
	"context"
	"reflect"
	"testing"

	"github.com/honmaple/cloudfs"
)

type paginationListFS struct {
	cloudfs.BaseFS
	files    []cloudfs.FileInfo
	listPath string
}

func (d *paginationListFS) List(_ context.Context, path string) ([]cloudfs.FileInfo, error) {
	d.listPath = path
	return d.files, nil
}

func TestPaginationFSList(t *testing.T) {
	base := &paginationListFS{
		files: []cloudfs.FileInfo{
			(&cloudfs.Entry{Name: "a"}).FileInfo(),
			(&cloudfs.Entry{Name: "b"}).FileInfo(),
			(&cloudfs.Entry{Name: "c"}).FileInfo(),
			(&cloudfs.Entry{Name: "d"}).FileInfo(),
			(&cloudfs.Entry{Name: "e"}).FileInfo(),
		},
	}

	fs, err := PaginationFS(&PaginationOption{})(base)
	if err != nil {
		t.Fatal(err)
	}

	files, err := fs.List(context.Background(), "/root?filter=x&page=2&page_size=2")
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name())
	}
	if !reflect.DeepEqual(names, []string{"c", "d"}) {
		t.Fatalf("names = %v, want [c d]", names)
	}
	if base.listPath != "/root?filter=x" {
		t.Fatalf("listPath = %q, want %q", base.listPath, "/root?filter=x")
	}
}

func TestPaginationFSListWithoutPageSize(t *testing.T) {
	base := &paginationListFS{
		files: []cloudfs.FileInfo{
			(&cloudfs.Entry{Name: "a"}).FileInfo(),
		},
	}

	fs, err := PaginationFS(&PaginationOption{})(base)
	if err != nil {
		t.Fatal(err)
	}

	files, err := fs.List(context.Background(), "/root?page=2")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
	if base.listPath != "/root?page=2" {
		t.Fatalf("listPath = %q, want %q", base.listPath, "/root?page=2")
	}
}
