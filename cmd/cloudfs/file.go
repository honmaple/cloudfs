package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/urfave/cli/v2"
)

func listAction(c *cli.Context, drivers *driverSet) error {
	value := c.Args().First()
	if value == "" {
		value = "."
	}
	fsys, p, _, err := drivers.resolve(value)
	if err != nil {
		return err
	}
	files, err := fsys.List(c.Context, p)
	if err != nil {
		return err
	}
	out := tabwriter.NewWriter(c.App.Writer, 0, 4, 2, ' ', 0)
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

func statAction(c *cli.Context, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	info, err := fsys.Stat(c.Context, p)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.App.Writer).Encode(struct {
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

func catAction(c *cli.Context, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	reader, err := fsys.Open(c.Context, p)
	if err != nil {
		return err
	}
	_, err = ioutil.CopyWithContext(c.Context, c.App.Writer, reader)
	return errors.Join(err, reader.Close())
}

func mkdirAction(c *cli.Context, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	return fsys.MakeDir(c.Context, p)
}

func renameAction(c *cli.Context, drivers *driverSet) error {
	name := c.Args().Get(1)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return errors.New("rename requires a single file name")
	}
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	return fsys.Rename(c.Context, p, name)
}

func removeAction(c *cli.Context, drivers *driverSet) error {
	fsys, p, _, err := drivers.resolve(c.Args().First())
	if err != nil {
		return err
	}
	clean, _ := cloudfs.ParsePath(p)
	if path.Clean(clean) == "/" {
		return errors.New("refusing to remove filesystem root")
	}
	info, err := fsys.Stat(c.Context, p)
	if err != nil {
		return err
	}
	if info.IsDir() && !c.Bool("recursive") {
		return errors.New("directory removal requires -r")
	}
	return fsys.Remove(c.Context, p)
}

func copyAction(c *cli.Context, drivers *driverSet) error {
	pair, err := drivers.pair(c.Args().Get(0), c.Args().Get(1))
	if err != nil {
		return err
	}
	return cloudfs.CopyBetween(c.Context, pair.srcFS, pair.src, pair.dstFS, pair.dst)
}

func moveAction(c *cli.Context, drivers *driverSet) error {
	pair, err := drivers.pair(c.Args().Get(0), c.Args().Get(1))
	if err != nil {
		return err
	}
	if path.Clean(pair.src) == "/" {
		return errors.New("refusing to move filesystem root")
	}
	if err = cloudfs.CopyBetween(c.Context, pair.srcFS, pair.src, pair.dstFS, pair.dst); err != nil {
		return err
	}
	return pair.srcFS.Remove(c.Context, pair.src)
}
