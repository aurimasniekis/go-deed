// Adapted from Porch internal/httpx/requestid.go (Apache-2.0).

package httpx

import (
	"context"
	"net/http"

	"github.com/aurimasniekis/go-deed/internal/ids"
)

type requestIDKey struct{}

// RequestID assigns every request a fresh ID (inbound X-Request-ID headers are ignored: they are client-controlled) and
// echoes it in the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ids.New(ids.Request)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// GetRequestID returns the request ID assigned by RequestID.
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
