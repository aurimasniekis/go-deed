package main

// TEMPORARY during development: replaced by the backup, import and demo slices before release.

import (
	"context"
	"errors"
	"io"
)

var errPending = errors.New("not available in this build yet")

func backupCommands() []*command {
	return []*command{{name: "run", summary: "take a backup now", run: pendingCmd}}
}

func runRestore(context.Context, []string, io.Writer, io.Writer) error  { return errPending }
func runDemoSeed(context.Context, []string, io.Writer, io.Writer) error { return errPending }
func pendingCmd(context.Context, []string, io.Writer, io.Writer) error  { return errPending }
