package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/middleware"
	"github.com/honmaple/cloudfs/syncer"
	"github.com/urfave/cli/v3"
)

var syncFlags = []cli.Flag{
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
	&cli.BoolFlag{
		Name:  "checksum",
		Usage: "Compare file contents using hashes, reusing saved hashes when possible",
	},
	&cli.IntFlag{
		Name:  "concurrency",
		Value: 1,
		Usage: "Parallel copies and file removals (requires concurrency-safe drivers and middleware)",
	},
	&cli.StringFlag{
		Name:  "conflict",
		Value: "abort",
		Usage: "Conflict policy: abort, skip, src, dst, newer, both (bisync only)",
	},
	&cli.StringFlag{
		Name:  "state",
		Usage: "Local state file (required for bisync)",
	},
}

func syncAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	mode := syncer.ModeCopy
	if c.Bool("mirror") {
		mode = syncer.ModeMirror
	}
	s := &syncRun{
		cli:     c,
		drivers: drivers,
	}
	return s.run(ctx, syncer.Option{
		Mode: mode,
	})
}

func bisyncAction(ctx context.Context, c *cli.Command, drivers *driverSet) error {
	if c.String("state") == "" {
		return errors.New("bisync requires --state")
	}
	s := &syncRun{
		cli:     c,
		drivers: drivers,
	}
	return s.run(ctx, syncer.Option{
		Mode: syncer.ModeTwoWay,
	})
}

type syncRun struct {
	cli            *cli.Command
	drivers        *driverSet
	phaseStartedAt time.Time
}

func (s *syncRun) run(ctx context.Context, option syncer.Option) error {
	handlers := map[string]syncer.ConflictHandler{
		"abort": syncer.Abort(),
		"skip":  syncer.Skip(),
		"src":   syncer.PreferSrc(),
		"dst":   syncer.PreferDst(),
		"both":  syncer.KeepBoth(),
		"newer": syncer.PreferNewer(),
	}
	onConflict, ok := handlers[s.cli.String("conflict")]
	if !ok {
		return errors.New("conflict must be abort, skip, src, dst, newer, or both")
	}
	pair, err := s.drivers.pair(s.cli.Args().Get(0), s.cli.Args().Get(1))
	if err != nil {
		return err
	}
	srcFS, err := cloudfs.New(pair.srcFS, middleware.PrefixFS(path.Clean(pair.src)))
	if err != nil {
		return err
	}
	dstFS, err := cloudfs.New(pair.dstFS, middleware.PrefixFS(path.Clean(pair.dst)))
	if err != nil {
		return err
	}
	if filename := s.cli.String("state"); filename != "" {
		option.StateStore = syncer.NewFileStore(
			filename,
			s.drivers.identity(pair.srcName, pair.src),
			s.drivers.identity(pair.dstName, pair.dst),
		)
	}
	option.DryRun = s.cli.Bool("dry-run")
	option.Concurrency = s.cli.Int("concurrency")
	option.MaxDelete = s.cli.Int("max-delete")
	option.MaxDeletePercent = s.cli.Int("max-delete-percent")
	option.Checksum = s.cli.Bool("checksum")
	option.OnConflict = onConflict
	option.OnEvent = s.onEvent
	return syncer.Sync(ctx, srcFS, dstFS, option)
}

func (s *syncRun) onEvent(e syncer.Event) {
	switch event := e.(type) {
	case syncer.SyncEvent:
		switch event.Status {
		case syncer.StatusStarted:
			s.phaseStartedAt = time.Now()
			if event.Phase != syncer.PhaseFinish {
				fmt.Fprintln(s.cli.Writer, event)
			}
		case syncer.StatusSucceeded, syncer.StatusFailed:
			elapsed := time.Since(s.phaseStartedAt).Round(time.Microsecond)
			fmt.Fprintf(s.cli.Writer, "%s elapsed=%s\n", event, elapsed)
		}
	case syncer.TaskEvent:
		fmt.Fprintln(s.cli.Writer, event)
	}
}
