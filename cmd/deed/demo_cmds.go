package main

import (
	"context"
	"io"
	"time"

	"github.com/aurimasniekis/go-deed/internal/demo"
)

func runDemoSeed(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("demo seed", stderr)
	cfgPath := configFlag(fs)
	scale := fs.Int("scale", 0, "extra synthetic hosts (each with three workloads) for load and benchmark runs")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr, false)
	if err != nil {
		return err
	}
	defer a.Close()
	res, err := demo.Seed(ctx, a.Catalog, a.Store, time.Now(), *scale)
	if err != nil {
		return err
	}
	return printJSON(stdout, res)
}
