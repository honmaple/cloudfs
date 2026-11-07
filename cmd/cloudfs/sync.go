package main

import (
	"errors"
	"fmt"
	"path"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/middleware"
	"github.com/honmaple/cloudfs/syncer"
	"github.com/urfave/cli/v2"
)

var syncFlags = []cli.Flag{
	&cli.IntFlag{
		Name:  "concurrency",
		Value: 1,
		Usage: "Parallel copies and file removals (requires concurrency-safe drivers and middleware)",
	},
	&cli.BoolFlag{
		Name:  "dry-run",
		Usage: "Preview planned operations without changing files or state",
	},
	&cli.IntFlag{
		Name:  "max-delete",
		Usage: "Maximum removed entries, including directory contents (0 is unlimited)",
	},
	&cli.IntFlag{
		Name:  "max-delete-percent",
		Usage: "Maximum percentage removed from either side (0 disables the limit)",
	},
	&cli.StringFlag{
		Name:  "state",
		Usage: "Local state file (required for bisync)",
	},
	&cli.BoolFlag{
		Name:  "checksum",
		Usage: "Compare file contents using hashes, reusing saved hashes when possible",
	},
	&cli.StringFlag{
		Name:  "conflict",
		Value: "abort",
		Usage: "Conflict policy: abort, skip, src, dst, newer, both (bisync only)",
	},
}

// prepareSync resolves directory roots, conflict handler and optional state.
func prepareSync(c *cli.Context, drivers *driverSet) (cloudfs.FS, cloudfs.FS, syncer.ConflictHandler, syncer.StateStore, error) {
	handlers := map[string]syncer.ConflictHandler{
		"abort": syncer.Abort(),
		"skip":  syncer.Skip(),
		"src":   syncer.PreferSrc(),
		"dst":   syncer.PreferDst(),
		"both":  syncer.KeepBoth(),
		"newer": syncer.PreferNewer(),
	}
	onConflict, ok := handlers[c.String("conflict")]
	if !ok {
		return nil, nil, nil, nil, errors.New("conflict must be abort, skip, src, dst, newer, or both")
	}
	pair, err := drivers.pair(c.Args().Get(0), c.Args().Get(1))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	srcFS, err := cloudfs.New(pair.srcFS, middleware.PrefixFS(path.Clean(pair.src)))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	dstFS, err := cloudfs.New(pair.dstFS, middleware.PrefixFS(path.Clean(pair.dst)))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var store syncer.StateStore
	if filename := c.String("state"); filename != "" {
		store = syncer.NewFileStore(
			filename,
			drivers.identity(pair.srcName, pair.src),
			drivers.identity(pair.dstName, pair.dst),
		)
	}
	return srcFS, dstFS, onConflict, store, nil
}

func syncAction(c *cli.Context, drivers *driverSet) error {
	srcFS, dstFS, onConflict, store, err := prepareSync(c, drivers)
	if err != nil {
		return err
	}
	return syncer.Sync(c.Context, srcFS, dstFS, syncer.Option{
		Concurrency:      c.Int("concurrency"),
		DryRun:           c.Bool("dry-run"),
		MaxDelete:        c.Int("max-delete"),
		MaxDeletePercent: c.Int("max-delete-percent"),
		OnEvent:          syncPreview(c),
		StateStore:       store,

		Delete:     c.Bool("delete"),
		Checksum:   c.Bool("checksum"),
		OnConflict: onConflict,
	})
}

func bisyncAction(c *cli.Context, drivers *driverSet) error {
	if c.String("state") == "" {
		return errors.New("bisync requires --state")
	}
	srcFS, dstFS, onConflict, store, err := prepareSync(c, drivers)
	if err != nil {
		return err
	}
	return syncer.Sync(c.Context, srcFS, dstFS, syncer.Option{
		Mode:             syncer.ModeTwoWay,
		Concurrency:      c.Int("concurrency"),
		DryRun:           c.Bool("dry-run"),
		MaxDelete:        c.Int("max-delete"),
		MaxDeletePercent: c.Int("max-delete-percent"),
		OnEvent:          syncPreview(c),
		StateStore:       store,
		Checksum:         c.Bool("checksum"),
		OnConflict:       onConflict,
	})
}

func syncPreview(c *cli.Context) syncer.EventHandler {
	if !c.Bool("dry-run") {
		return nil
	}
	return func(notification syncer.Event) {
		switch event := notification.(type) {
		case syncer.SyncEvent:
			info, side := event.FileInfo, "target"
			if event.Reverse {
				side = "source"
			}
			if event.NewPath != "" {
				fmt.Fprintf(c.App.Writer, "%s %s %s -> %s\n", event.Op, side, info.Path(), event.NewPath)
			} else {
				fmt.Fprintf(c.App.Writer, "%s %s %s\n", event.Op, side, info.Path())
			}
		}
	}
}
