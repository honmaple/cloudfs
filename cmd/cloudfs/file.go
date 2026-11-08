package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/urfave/cli/v3"
)

func listAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	value := c.Args().First()
	if value == "" {
		value = "."
	}
	fsys, p, _, err := drivers.resolve(value)
	if err != nil {
		return err
	}
	files, err := fsys.List(ctx, p)
	if err != nil {
		return err
	}
	out := tabwriter.NewWriter(c.Writer, 0, 4, 2, ' ', 0)
	if _, err = fmt.Fprintln(out, "TYPE\tSIZE\tMODIFIED\tNAME"); err != nil {
		return err
	}
	for _, file := range files {
		kind := "file"
		if file.IsDir() {
			kind = "dir"
		}
		if _, err = fmt.Fprintf(out, "%s\t%d\t%s\t%q\n", kind, file.Size(), file.ModTime().UTC().Format(time.RFC3339), file.Name()); err != nil {
			return err
		}
	}
	return out.Flush()
}

func statAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	info, err := fsys.Stat(ctx, p)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.Writer).Encode(struct {
		Path    string    `json:"path"`
		Name    string    `json:"name"`
		Size    int64     `json:"size"`
		IsDir   bool      `json:"is_dir"`
		ModTime time.Time `json:"mod_time"`
	}{
		p,
		info.Name(),
		info.Size(),
		info.IsDir(),
		info.ModTime(),
	})
}

func catAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	reader, err := fsys.Open(ctx, p)
	if err != nil {
		return err
	}
	_, err = ioutil.CopyWithContext(ctx, c.Writer, reader)
	return errors.Join(err, reader.Close())
}

func mkdirAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	return fsys.MakeDir(ctx, p)
}

func renameAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	name := c.Args().Get(1)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return errors.New("rename requires a single file name")
	}
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	return fsys.Rename(ctx, p, name)
}

func removeAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	clean, _ := cloudfs.ParsePath(p)
	if path.Clean(clean) == "/" {
		return errors.New("refusing to remove filesystem root")
	}
	info, err := fsys.Stat(ctx, p)
	if err != nil {
		return err
	}
	if info.IsDir() && !c.Bool("recursive") {
		return errors.New("directory removal requires -r")
	}
	return fsys.Remove(ctx, p)
}

func copyAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	pair, err := drivers.pair(c.Args().Get(0), c.Args().Get(1))
	if err != nil {
		return err
	}
	return cloudfs.CopyBetween(ctx, pair.srcFS, pair.src, pair.dstFS, pair.dst)
}

func moveAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	pair, err := drivers.pair(c.Args().Get(0), c.Args().Get(1))
	if err != nil {
		return err
	}
	if path.Clean(pair.src) == "/" {
		return errors.New("refusing to move filesystem root")
	}
	if err = cloudfs.CopyBetween(ctx, pair.srcFS, pair.src, pair.dstFS, pair.dst); err != nil {
		return err
	}
	return pair.srcFS.Remove(ctx, pair.src)
}
