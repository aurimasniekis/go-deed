// Package testenv builds an in-process Deed core (store, keyring, audit, catalog) for tests.
package testenv

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
	"github.com/aurimasniekis/go-deed/internal/store/storetest"
)

// Env is a test environment.
type Env struct {
	Store   *store.Store
	Keyring *keyring.Keyring
	Audit   *audit.Writer
	Catalog *catalog.Service
	Clock   *clock.Fake
}

// New opens a fresh environment with built-in kinds seeded.
func New(t testing.TB) *Env {
	t.Helper()
	c := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	s := storetest.Open(t, storetest.WithClock(c))
	k, err := keyring.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	w, err := keyring.NewLocalKEK(k)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	kr, err := keyring.Load(ctx, s, w)
	if err != nil {
		t.Fatal(err)
	}
	aw := audit.NewWriter(kr, nil)
	cat := catalog.New(s, aw, kr, c, nil, nil)
	if err := cat.SeedBuiltins(ctx); err != nil {
		t.Fatal(err)
	}
	return &Env{Store: s, Keyring: kr, Audit: aw, Catalog: cat, Clock: c}
}

// Project creates a project.
func (e *Env) Project(t testing.TB, name string) *catalog.Project {
	t.Helper()
	p, err := e.Catalog.CreateProject(context.Background(), audit.System, catalog.ProjectInput{Name: name}, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Source returns a system source of a project, or creates a custom one when layer is set.
func (e *Env) Source(t testing.TB, projectID, name string, layer catalog.Layer) *catalog.Source {
	t.Helper()
	ctx := context.Background()
	if layer == "" {
		s, err := e.Catalog.SourceByName(ctx, projectID, name)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s, err := e.Catalog.CreateSource(ctx, audit.System, catalog.SourceInput{ProjectID: projectID, Name: name, Provider: "push", Layer: layer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Apply writes one entity item in its own transaction.
func (e *Env) Apply(t testing.TB, src *catalog.Source, it catalog.EntityItem, o catalog.ApplyOpts) (*catalog.ApplyResult, error) {
	t.Helper()
	var res *catalog.ApplyResult
	err := e.Store.W().Tx(context.Background(), func(tx *store.Tx) error {
		var err error
		res, err = e.Catalog.ApplyEntity(context.Background(), tx, src, &it, o)
		return err
	})
	return res, err
}

// Relate writes one relation item in its own transaction.
func (e *Env) Relate(t testing.TB, src *catalog.Source, it catalog.RelationItem) (*catalog.RelationResult, error) {
	t.Helper()
	var res *catalog.RelationResult
	err := e.Store.W().Tx(context.Background(), func(tx *store.Tx) error {
		var err error
		res, err = e.Catalog.ApplyRelation(context.Background(), tx, src, &it, catalog.ApplyOpts{})
		return err
	})
	return res, err
}
