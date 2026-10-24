// Package versions computes version intelligence (§13): for every entity with a declared or observed version it
// records the newest available version and end-of-life status in version_checks, with the evidence used. Two sources
// are supported:
//
//   - the catalog: an entity deployed_from a repository is compared with that repository's newest non-prerelease
//     release (no network access);
//   - an endoflife.date-compatible API (opt-in, versions.eol): the release cycle of metadata.version.product that
//     matches the version gives its latest patch release and end-of-life date.
//
// Results feed the version_update_available and version_end_of_life findings.
package versions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/safehttp"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Statuses stored in version_checks.
const (
	StatusCurrent         = "current"
	StatusUpdateAvailable = "update_available"
	StatusEOL             = "eol"
	StatusUnknown         = "unknown"
	StatusNotApplicable   = "not_applicable"
)

// Cycle is one release cycle of a product (endoflife.date's /api/<product>.json entries).
type Cycle struct {
	Cycle       string `json:"cycle"`
	Latest      string `json:"latest"`
	LatestDate  string `json:"latestReleaseDate"`
	ReleaseDate string `json:"releaseDate"`
	// EOL and Support are a date (YYYY-MM-DD) or a boolean in the API.
	EOL     json.RawMessage `json:"eol"`
	Support json.RawMessage `json:"support"`
	LTS     json.RawMessage `json:"lts"`
}

// dateOrBool decodes endoflife.date's date-or-boolean fields: a date string, or a boolean flag.
func dateOrBool(raw json.RawMessage) (date string, flag bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	_ = json.Unmarshal(raw, &flag)
	return "", flag
}

// Checker evaluates entity versions.
type Checker struct {
	Store   *store.Store
	Guard   *safehttp.Guard
	Clock   clock.Clock
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// EOLURL enables the end-of-life source (empty = catalog only).
	EOLURL string
	// MaxProducts bounds API lookups per run (default 100).
	MaxProducts int

	mu    sync.Mutex
	cache map[string]cachedProduct
}

type cachedProduct struct {
	cycles []Cycle
	err    error
	at     time.Time
}

// productRE limits product names to endoflife.date's slug alphabet before they reach a URL.
var productRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,63}$`)

func (c *Checker) product(ctx context.Context, name string) ([]Cycle, error) {
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cachedProduct{}
	}
	if p, ok := c.cache[name]; ok && c.Clock.Now().Sub(p.at) < 12*time.Hour {
		c.mu.Unlock()
		return p.cycles, p.err
	}
	c.mu.Unlock()
	u, err := url.Parse(c.EOLURL)
	if err != nil {
		return nil, err
	}
	hc, err := c.Guard.Client(safehttp.Policy{Allow: []string{u.Scheme + "://" + u.Host}, Timeout: 20 * time.Second, MaxBodyBytes: 2 << 20, MaxRedirects: -1})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.EOLURL, "/")+"/api/"+url.PathEscape(name)+".json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "deed-versions")
	var cycles []Cycle
	resp, err := hc.Do(req)
	if err == nil {
		switch {
		case resp.StatusCode == http.StatusNotFound:
			err = fmt.Errorf("product %q is not known to the EOL source", name)
		case resp.StatusCode != http.StatusOK:
			err = fmt.Errorf("EOL source answered %d", resp.StatusCode)
		default:
			if derr := json.NewDecoder(resp.Body).Decode(&cycles); derr != nil {
				err = fmt.Errorf("EOL source returned an unexpected body")
			}
		}
		resp.Body.Close()
	} else {
		err = fmt.Errorf("EOL source unreachable: %s", safehttp.Classify(err))
	}
	c.mu.Lock()
	c.cache[name] = cachedProduct{cycles: cycles, err: err, at: c.Clock.Now()}
	c.mu.Unlock()
	return cycles, err
}

// Normalize strips a leading "v" and build metadata.
func Normalize(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	return v
}

var partRE = regexp.MustCompile(`[0-9]+|[A-Za-z]+`)

// Compare orders versions part by part (numbers numerically, words lexically; a pre-release word sorts before the
// release: 1.2.0-rc1 < 1.2.0). It returns -1, 0 or 1.
func Compare(a, b string) int {
	pa, pb := partRE.FindAllString(Normalize(a), -1), partRE.FindAllString(Normalize(b), -1)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		switch {
		case i >= len(pa):
			if isNum(pb[i]) {
				return -1
			}
			return 1 // b has a pre-release suffix
		case i >= len(pb):
			if isNum(pa[i]) {
				return 1
			}
			return -1
		}
		x, y := pa[i], pb[i]
		if isNum(x) && isNum(y) {
			nx, _ := strconv.ParseUint(strings.TrimLeft(x, "0")+"0", 10, 64)
			ny, _ := strconv.ParseUint(strings.TrimLeft(y, "0")+"0", 10, 64)
			if nx != ny {
				if nx < ny {
					return -1
				}
				return 1
			}
			continue
		}
		if isNum(x) != isNum(y) {
			if isNum(x) {
				return 1
			}
			return -1
		}
		if c := strings.Compare(strings.ToLower(x), strings.ToLower(y)); c != 0 {
			return c
		}
	}
	return 0
}

func isNum(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

// MatchCycle finds the cycle a version belongs to: the longest cycle that equals the version or is a dotted prefix
// of it ("15.4" → "15", "22.04.3" → "22.04").
func MatchCycle(cycles []Cycle, version string) (Cycle, bool) {
	v := Normalize(version)
	best, found := Cycle{}, false
	for _, c := range cycles {
		cy := Normalize(c.Cycle)
		if cy == v || strings.HasPrefix(v, cy+".") || strings.HasPrefix(v, cy+"-") {
			if !found || len(cy) > len(Normalize(best.Cycle)) {
				best, found = c, true
			}
		}
	}
	return best, found
}

// Result is one entity's check.
type Result struct {
	EntityID     string
	Product      string
	Cycle        string
	Latest       string
	LatestSource string
	EOL          string // "true", "false" or a date
	EOLDate      string
	SupportUntil string
	Status       string
	Evidence     map[string]any
}

// evaluate decides the status of current against the available data.
func evaluate(current string, now time.Time, latest, latestSource string, cyc *Cycle) Result {
	r := Result{Status: StatusUnknown, Evidence: map[string]any{"current": current}}
	if current == "" {
		r.Status = StatusNotApplicable
		return r
	}
	if cyc != nil {
		r.Cycle, r.Latest, r.LatestSource = cyc.Cycle, cyc.Latest, "eol_api"
		r.Evidence["cycle"], r.Evidence["cycle_latest"] = cyc.Cycle, cyc.Latest
		if d, flag := dateOrBool(cyc.EOL); d != "" {
			r.EOL, r.EOLDate = d, d
		} else {
			r.EOL = strconv.FormatBool(flag)
		}
		r.Evidence["eol"] = r.EOL
		if d, _ := dateOrBool(cyc.Support); d != "" {
			r.SupportUntil = d
		}
	}
	if latest != "" && (r.Latest == "" || Compare(latest, r.Latest) > 0 && latestSource == "catalog_release") {
		r.Latest, r.LatestSource = latest, latestSource
		r.Evidence["latest_release"] = latest
	}
	eol := r.EOL == "true"
	if r.EOLDate != "" {
		if t, err := time.Parse("2006-01-02", r.EOLDate); err == nil && !now.Before(t) {
			eol = true
		}
	}
	switch {
	case eol:
		r.Status = StatusEOL
	case r.Latest != "" && Compare(current, r.Latest) < 0:
		r.Status = StatusUpdateAvailable
	case r.Latest != "":
		r.Status = StatusCurrent
	case cyc != nil:
		r.Status = StatusCurrent
	}
	return r
}

type candidate struct {
	id, observed, declared, product string
}

// Run checks every live entity that has a version and writes version_checks and entities.version_available.
func (c *Checker) Run(ctx context.Context) (int, error) {
	d := c.Store.R()
	rows, err := d.QueryContext(ctx, `SELECT id, version_observed, version_declared,
		COALESCE(json_extract(effective_json, '$.metadata.version.product'), '') FROM entities
		WHERE tombstoned_at IS NULL AND (version_observed != '' OR version_declared != '')`)
	if err != nil {
		return 0, err
	}
	var cands []candidate
	for rows.Next() {
		var x candidate
		if err := rows.Scan(&x.id, &x.observed, &x.declared, &x.product); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, x)
	}
	rows.Close()
	// Newest non-prerelease release of the repository each entity is deployed from (catalog-only source).
	releases := map[string]string{}
	rrows, err := d.QueryContext(ctx, `SELECT r.from_id, json_extract(rel.effective_json, '$.spec.tag')
		FROM relations r
		JOIN relations rr ON rr.from_id = r.to_id AND rr.type = 'contains' AND rr.live = 1
		JOIN entities rel ON rel.id = rr.to_id AND rel.kind = 'release' AND rel.tombstoned_at IS NULL
		WHERE r.type = 'deployed_from' AND r.live = 1
		  AND COALESCE(json_extract(rel.effective_json, '$.spec.prerelease'), 0) = 0
		  AND COALESCE(json_extract(rel.effective_json, '$.spec.draft'), 0) = 0`)
	if err != nil {
		return 0, err
	}
	for rrows.Next() {
		var from string
		var tag sql.NullString
		if err := rrows.Scan(&from, &tag); err != nil {
			rrows.Close()
			return 0, err
		}
		if tag.Valid && (releases[from] == "" || Compare(tag.String, releases[from]) > 0) {
			releases[from] = tag.String
		}
	}
	rrows.Close()
	now := c.Clock.Now()
	max := c.MaxProducts
	if max <= 0 {
		max = 100
	}
	looked := map[string]bool{}
	var results []Result
	for _, x := range cands {
		current := x.observed
		if current == "" {
			current = x.declared
		}
		var cyc *Cycle
		var lookupErr string
		if c.EOLURL != "" && x.product != "" {
			switch {
			case !productRE.MatchString(x.product):
				lookupErr = "invalid product name"
			case !looked[x.product] && len(looked) >= max:
				lookupErr = "product lookup budget reached"
			default:
				looked[x.product] = true
				cycles, err := c.product(ctx, x.product)
				if err != nil {
					lookupErr = err.Error()
				} else if m, ok := MatchCycle(cycles, current); ok {
					cyc = &m
				} else {
					lookupErr = "no release cycle matches " + current
				}
			}
		}
		latestSource := ""
		if releases[x.id] != "" {
			latestSource = "catalog_release"
		}
		r := evaluate(current, now, releases[x.id], latestSource, cyc)
		r.EntityID, r.Product = x.id, x.product
		if lookupErr != "" {
			r.Evidence["eol_source_error"] = lookupErr
		}
		if x.declared != "" && x.observed != "" && x.declared != x.observed {
			r.Evidence["declared"] = x.declared
		}
		results = append(results, r)
	}
	err = c.Store.W().TxNamed(ctx, "versions.write", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM version_checks WHERE entity_id NOT IN (SELECT id FROM entities WHERE tombstoned_at IS NULL
			AND (version_observed != '' OR version_declared != ''))`); err != nil {
			return err
		}
		for _, r := range results {
			ev, _ := json.Marshal(r.Evidence)
			if _, err := tx.Exec(ctx, `INSERT INTO version_checks (entity_id, product, cycle, latest, latest_source, eol, eol_date, support_until, status,
				evidence_json, checked_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (entity_id) DO UPDATE SET product = excluded.product,
				cycle = excluded.cycle, latest = excluded.latest, latest_source = excluded.latest_source, eol = excluded.eol, eol_date = excluded.eol_date,
				support_until = excluded.support_until, status = excluded.status, evidence_json = excluded.evidence_json, checked_at = excluded.checked_at`,
				r.EntityID, r.Product, r.Cycle, r.Latest, r.LatestSource, r.EOL, r.EOLDate, r.SupportUntil, r.Status, string(ev), tx.NowMicros()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE entities SET version_available = ? WHERE id = ? AND version_available != ?`, r.Latest, r.EntityID, r.Latest); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(results), nil
}
