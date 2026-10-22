// Command cloudfs operates on local and cloud filesystems.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/urfave/cli/v2"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	app := &cli.App{
		Name:   "cloudfs",
		Usage:  "Operate on local and cloud filesystems",
		Reader: stdin, Writer: stdout, ErrWriter: stderr,
		HideVersion: true,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "Load named drivers from FILE (default: config.json/yaml/yml/toml in current directory)",
			},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unknown command %q", c.Args().First())
			}
			return cli.ShowAppHelp(c)
		},
		Commands: []*cli.Command{
			{
				Name:      "stat",
				Usage:     "Print file metadata as JSON",
				ArgsUsage: "PATH",
				Action:    commandAction(1, 1, statAction),
			},
			{
				Name:      "cat",
				Usage:     "Read file to stdout",
				ArgsUsage: "PATH",
				Action:    commandAction(1, 1, catAction),
			},
			{
				Name:      "ls",
				Usage:     "List directory",
				ArgsUsage: "[PATH]",
				Action:    commandAction(0, 1, listAction),
			},
			{
				Name:      "mkdir",
				Usage:     "Create directory",
				ArgsUsage: "PATH",
				Action:    commandAction(1, 1, mkdirAction),
			},
			{
				Name:      "rename",
				Usage:     "Rename within parent directory",
				ArgsUsage: "PATH NAME",
				Action:    commandAction(2, 2, renameAction),
			},
			{
				Name:      "cp",
				Usage:     "Copy file or directory to exact destination path",
				ArgsUsage: "SOURCE DESTINATION",
				Action:    commandAction(2, 2, copyAction),
			},
			{
				Name:      "mv",
				Usage:     "Copy to exact destination, then remove source",
				ArgsUsage: "SOURCE DESTINATION",
				Action:    commandAction(2, 2, moveAction),
			},
			{
				Name:      "rm",
				Usage:     "Remove file or directory",
				ArgsUsage: "PATH",
				Action:    commandAction(1, 1, removeAction),
				Flags: []cli.Flag{&cli.BoolFlag{
					Name:    "recursive",
					Aliases: []string{"r"},
					Usage:   "Permit directory removal",
				}},
			},
			{
				Name:      "sync",
				Usage:     "Synchronize source directory to target",
				ArgsUsage: "SOURCE DESTINATION",
				Action:    commandAction(2, 2, syncAction),
				Flags: append(syncFlags, &cli.BoolFlag{
					Name:  "delete",
					Usage: "Delete target-only entries",
				}),
			},
			{
				Name:      "bisync",
				Usage:     "Synchronize directories in both directions",
				ArgsUsage: "SOURCE DESTINATION",
				Action:    commandAction(2, 2, bisyncAction),
				Flags:     syncFlags,
			},
		},
	}
	return app.RunContext(ctx, append([]string{"cloudfs"}, args...))
}

// commandAction owns validation and driver lifetime for every command.
func commandAction(minArgs, maxArgs int, action func(*cli.Context, *driverSet) error) cli.ActionFunc {
	return func(c *cli.Context) error {
		if c.NArg() < minArgs || c.NArg() > maxArgs {
			return fmt.Errorf("%s expects %s (see help)", c.Command.Name, c.Command.ArgsUsage)
		}
		if err := c.Context.Err(); err != nil {
			return err
		}
		drivers, err := loadDrivers(c.String("config"))
		if err != nil {
			return err
		}
		err = action(c, drivers)
		if err != nil {
			err = fmt.Errorf("%s: %w", c.Command.Name, err)
		}
		return errors.Join(err, drivers.close())
	}
}
