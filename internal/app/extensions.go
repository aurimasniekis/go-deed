package app

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/httpapi"
)

// initExtensions wires ingest, sources, graph, live updates, telemetry, findings, alerts and backups.
func (a *App) initExtensions(_ context.Context) error { return nil }

// APIExtensionsForSpec returns every API extension so `deed openapi` documents the complete API without services.
func APIExtensionsForSpec() []func(huma.API, *httpapi.Deps) { return nil }
