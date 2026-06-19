package cloudfs

import (
	"context"
	"errors"
	"io/fs"
	stdpath "path"

	"github.com/honmaple/cloudfs/utils/ioutil"
)

type (
	WalkDirFunc func(string, FileInfo, error) error
)

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

func Copy(ctx context.Context, srcFS FS, src, dst string, opts ...map[string]any) error {
	meta := NewOption(opts...)

	dstFile, err := srcFS.Stat(ctx, dst)
	if err != nil {
		// 复制并重命名
		if !meta.GetBool("auto_rename") {
			return err
		}
		_, err = srcFS.Stat(ctx, stdpath.Dir(dst))
		if err != nil {
			return err
		}
	} else if !dstFile.IsDir() {
		return &fs.PathError{Op: "copy", Path: dst, Err: errors.New("copy dst must be a dir")}
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

	files, err := srcFS.List(ctx, stdpath.Join(d.Path(), d.Name()))
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
