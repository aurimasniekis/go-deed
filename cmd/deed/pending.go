package main

// TEMPORARY during development: replaced by the backup, import and demo slices before release.

import (
	"context"
	"errors"
	"io"
)

var errPending = errors.New("not available in this build yet")

func runDemoSeed(context.Context, []string, io.Writer, io.Writer) error { return errPending }
