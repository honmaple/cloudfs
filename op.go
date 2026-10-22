package cloudfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	stdpath "path"
	"strings"

	"github.com/honmaple/cloudfs/utils/ioutil"
)

type (
	WalkDirFunc func(string, FileInfo, error) error
)

func copyFileBetween(ctx context.Context, srcFS FS, src string, dstFS FS, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := srcFS.Open(ctx, src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	writer, err := dstFS.Create(ctx, dst)
	if err != nil {
		return errors.Join(fmt.Errorf("create %s: %w", dst, err), reader.Close())
	}
	_, err = ioutil.CopyWithContext(ctx, writer, reader)
	return errors.Join(err, writer.Close(), reader.Close())
}

// CopyBetween copies a file or directory from srcFS to dstFS using streams.
// dst is the exact destination path; the source name is not appended. Directory
// contents merge into dst, preserving target-only entries and empty directories.
// File destination parents must exist; files may be overwritten. Symbolic links
// and other special files are rejected. Stream close errors are returned, and
// failure may leave partial destination content. Filesystems are not closed.
// The caller must ensure source and destination do not overlap, including when
// different filesystem instances refer to the same underlying storage.
func CopyBetween(ctx context.Context, srcFS FS, src string, dstFS FS, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := srcFS.Stat(ctx, src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return WalkDir(ctx, srcFS, src, func(p string, info FileInfo, err error) error {
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			relative := strings.TrimPrefix(strings.TrimPrefix(p, stdpath.Clean(src)), "/")
			dest := stdpath.Join(dst, relative)
			if info.IsDir() {
				return dstFS.MakeDir(ctx, dest)
			}
			return copyFileBetween(ctx, srcFS, p, dstFS, dest)
		})
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported file type: %s", src)
	}
	return copyFileBetween(ctx, srcFS, src, dstFS, dst)
}

func CopyFile(ctx context.Context, srcFS FS, src, dst string) error {
	srcFile, err := srcFS.Open(ctx, src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := srcFS.Create(ctx, dst)
	if err != nil {
		return err
	}

	if _, err := ioutil.CopyWithContext(ctx, dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}
	return dstFile.Close()
}

func CopyDir(ctx context.Context, srcFS FS, src, dst string) error {
	if err := srcFS.MakeDir(ctx, dst); err != nil {
		return err
	}

	files, err := srcFS.List(ctx, src)
	if err != nil {
		return err
	}

	for _, file := range files {
		srcPath := stdpath.Join(src, file.Name())
		dstPath := stdpath.Join(dst, file.Name())

		if file.IsDir() {
			err = CopyDir(ctx, srcFS, srcPath, dstPath)
		} else {
			err = CopyFile(ctx, srcFS, srcPath, dstPath)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func Copy(ctx context.Context, srcFS FS, src, dst string) error {
	dstFile, err := srcFS.Stat(ctx, dst)
	if err != nil {
		_, err = srcFS.Stat(ctx, stdpath.Dir(dst))
		if err != nil {
			return err
		}
	} else if !dstFile.IsDir() {
		return &fs.PathError{
			Op:   "copy",
			Path: dst,
			Err:  errors.New("copy dst must be a dir"),
		}
	} else {
		dst = stdpath.Join(dst, stdpath.Base(src))
	}

	srcFile, err := srcFS.Stat(ctx, src)
	if err != nil {
		return err
	}
	if srcFile.IsDir() {
		return CopyDir(ctx, srcFS, src, dst)
	}
	return CopyFile(ctx, srcFS, src, dst)
}

func walkDir(ctx context.Context, srcFS FS, root string, d FileInfo, walkDirFn WalkDirFunc) error {
	if err := walkDirFn(root, d, nil); err != nil || !d.IsDir() {
		if err == fs.SkipDir && d.IsDir() {
			err = nil
		}
		return err
	}

	files, err := srcFS.List(ctx, root)
	if err != nil {
		err = walkDirFn(root, d, err)
		if err != nil {
			if err == fs.SkipDir && d.IsDir() {
				err = nil
			}
			return err
		}
		return err
	}
	for _, file := range files {
		name := stdpath.Join(root, file.Name())
		if err := walkDir(ctx, srcFS, name, file, walkDirFn); err != nil {
			if err == fs.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}

func WalkDir(ctx context.Context, srcFS FS, root string, walkDirFn WalkDirFunc) error {
	info, err := srcFS.Stat(ctx, root)
	if err != nil {
		err = walkDirFn(root, nil, err)
	} else {
		err = walkDir(ctx, srcFS, root, info, walkDirFn)
	}
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}
