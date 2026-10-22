package ioutil

import (
	"context"
	"fmt"
	"io"
	"math"
)

type readerFunc func(p []byte) (n int, err error)

func (rf readerFunc) Read(p []byte) (n int, err error) { return rf(p) }

type writerFunc func([]byte) (int, error)

func (fn writerFunc) Write(p []byte) (int, error) { return fn(p) }

func Copy(ctx context.Context, dst io.Writer, src io.Reader, progress func(int64)) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if progress != nil {
		progress(0)
		writer := dst
		var written int64
		dst = writerFunc(func(p []byte) (int, error) {
			n, err := writer.Write(p)
			if n > 0 {
				written += int64(n)
				progress(written)
			}
			return n, err
		})
	}
	return io.Copy(dst, readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return src.Read(p)
	}))
}

func CopyN(ctx context.Context, dst io.Writer, src io.Reader, n int64, progress func(int64)) (int64, error) {
	return Copy(ctx, dst, io.LimitReader(src, n), progress)
}

func CopyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return Copy(ctx, dst, src, nil)
}

func CopyNWithContext(ctx context.Context, dst io.Writer, src io.Reader, n int64) (int64, error) {
	return CopyN(ctx, dst, src, n, nil)
}

func PrettyByteSize(b int) string {
	bf := float64(b)
	for _, unit := range []string{"", "Ki", "Mi", "Gi", "Ti", "Pi", "Ei", "Zi"} {
		if math.Abs(bf) < 1024.0 {
			return fmt.Sprintf("%3.1f%sB", bf, unit)
		}
		bf /= 1024.0
	}
	return fmt.Sprintf("%.1fYiB", bf)
}
