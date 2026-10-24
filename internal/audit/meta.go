// Adapted from Porch (Apache-2.0), which adapted HallPass internal/audit/meta.go.

package audit

import "context"

// Meta carries request metadata recorded on audit events.
type Meta struct {
	SourceIP   string
	RequestID  string
	AuthMethod string
}

type metaKey struct{}

// WithMeta attaches request metadata to ctx.
func WithMeta(ctx context.Context, m Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// MetaFrom returns metadata attached with WithMeta.
func MetaFrom(ctx context.Context) Meta {
	m, _ := ctx.Value(metaKey{}).(Meta)
	return m
}
