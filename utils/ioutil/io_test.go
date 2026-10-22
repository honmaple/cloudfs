package ioutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type partialWriter struct {
	err error
}

func (w partialWriter) Write([]byte) (int, error) { return 2, w.err }

func TestCopyProgressCountsWrittenBytes(t *testing.T) {
	cause := errors.New("write failed")
	var progress []int64
	written, err := Copy(context.Background(), partialWriter{
		err: cause,
	}, strings.NewReader("abcdef"), func(n int64) { progress = append(progress, n) })
	if written != 2 || !errors.Is(err, cause) || !reflect.DeepEqual(progress, []int64{0, 2}) {
		t.Fatalf("written=%d err=%v progress=%v", written, err, progress)
	}
	progress = nil
	_, err = Copy(context.Background(), io.Discard, strings.NewReader(""), func(n int64) { progress = append(progress, n) })
	if err != nil || !reflect.DeepEqual(progress, []int64{0}) {
		t.Fatalf("empty progress=%v err=%v", progress, err)
	}
}

func TestCopyProgressCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var dst bytes.Buffer
	_, err := Copy(ctx, &dst, strings.NewReader("data"), func(int64) { cancel() })
	if !errors.Is(err, context.Canceled) || dst.Len() != 0 {
		t.Fatalf("error=%v written=%d", err, dst.Len())
	}
}
