// Adapted from Porch internal/mgmtapi/problem.go (Apache-2.0).

package httpapi

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
)

// Problem is an RFC 9457 problem document with a stable machine-readable code.
type Problem struct {
	Type     string              `json:"type,omitempty" doc:"URI reference identifying the problem type."`
	Title    string              `json:"title,omitempty" doc:"Short summary of the problem type."`
	Status   int                 `json:"status,omitempty" doc:"HTTP status code."`
	Detail   string              `json:"detail,omitempty" doc:"Explanation of this occurrence."`
	Code     string              `json:"code" doc:"Stable error code, e.g. validation_failed, not_found, conflict, precondition_failed, stale_revision, reauth_required."`
	Fields   []apperr.FieldError `json:"fields,omitempty" doc:"Per-field validation problems."`
	Errors   []*huma.ErrorDetail `json:"errors,omitempty" doc:"Request-schema validation details."`
	Instance string              `json:"instance,omitempty"`
	Action   string              `json:"action,omitempty" doc:"What the client should do next (re-authentication URL)."`

	headers http.Header
}

// GetHeaders implements huma.HeadersError (Retry-After on 429/503).
func (p *Problem) GetHeaders() http.Header { return p.headers }

func writeProblem(api huma.API, ctx huma.Context, p *Problem) error {
	for k, v := range p.headers {
		ctx.SetHeader(k, v[0])
	}
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetHeader("Cache-Control", "no-store")
	ctx.SetStatus(p.Status)
	return api.Marshal(ctx.BodyWriter(), "application/json", p)
}

func (p *Problem) Error() string  { return p.Detail }
func (p *Problem) GetStatus() int { return p.Status }

// ContentType makes JSON problems application/problem+json.
func (p *Problem) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}

func problem(status int, code apperr.Code, detail string, fields []apperr.FieldError) *Problem {
	p := &Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, Code: string(code), Fields: fields}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		p.headers = http.Header{"Retry-After": []string{"5"}}
	}
	return p
}

// NewProblem builds a problem (extension packages).
func NewProblem(status int, code apperr.Code, detail string) *Problem {
	return problem(status, code, detail, nil)
}

func defaultCode(status int) apperr.Code {
	switch status {
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return apperr.CodeInvalid
	case http.StatusUnauthorized:
		return apperr.CodeUnauthorized
	case http.StatusForbidden:
		return apperr.CodeForbidden
	case http.StatusNotFound:
		return apperr.CodeNotFound
	case http.StatusConflict:
		return apperr.CodeConflict
	case http.StatusPreconditionFailed:
		return apperr.CodePrecondition
	case http.StatusTooManyRequests:
		return apperr.CodeRateLimited
	case http.StatusServiceUnavailable:
		return apperr.CodeUnavailable
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	}
	if status >= 500 {
		return "internal"
	}
	return apperr.Code(http.StatusText(status))
}

func init() {
	// Deed never returns nil slices, so arrays are not nullable in the schema (cleaner generated client types).
	huma.DefaultArrayNullable = false
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		p := problem(status, defaultCode(status), msg, nil)
		for _, e := range errs {
			if e == nil {
				continue
			}
			if d, ok := e.(huma.ErrorDetailer); ok {
				p.Errors = append(p.Errors, d.ErrorDetail())
			} else {
				p.Errors = append(p.Errors, &huma.ErrorDetail{Message: e.Error()})
			}
		}
		return p
	}
}
