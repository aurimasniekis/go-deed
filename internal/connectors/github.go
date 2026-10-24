package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/sources"
)

// GitHubType is the provider name of the GitHub connector.
const GitHubType = "github"

// GitHubConfig configures the GitHub connector.
type GitHubConfig struct {
	APIURL              string   `json:"api_url,omitempty"`
	Owners              []string `json:"owners,omitempty"`
	Repositories        []string `json:"repositories,omitempty"`
	Topics              []string `json:"topics,omitempty"`
	IncludeForks        bool     `json:"include_forks,omitempty"`
	IncludeArchived     bool     `json:"include_archived,omitempty"`
	Releases            *int     `json:"releases,omitempty"`
	Deployments         *bool    `json:"deployments,omitempty"`
	PullRequests        *bool    `json:"pull_requests,omitempty"`
	Issues              *bool    `json:"issues,omitempty"`
	ClosedRetentionDays int      `json:"closed_retention_days,omitempty"`
	MaxRepos            int      `json:"max_repos,omitempty"`
	MaxItemsPerRepo     int      `json:"max_items_per_repo,omitempty"`
	MinRateRemaining    int      `json:"min_rate_remaining,omitempty"`
	AllowCIDRs          []string `json:"allow_cidrs,omitempty"`
	InsecureHTTP        bool     `json:"insecure_http,omitempty"`
}

const githubSchema = `{
  "type": "object", "additionalProperties": false,
  "properties": {
    "api_url": {"type": "string", "format": "uri", "default": "https://api.github.com", "description": "REST API base (GitHub Enterprise Server: https://HOST/api/v3)."},
    "owners": {"type": "array", "maxItems": 50, "items": {"type": "string", "pattern": "^(@me|[A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))$"}, "description": "Organizations or users whose repositories are imported; @me is the token's own account (includes private repositories)."},
    "repositories": {"type": "array", "maxItems": 500, "items": {"type": "string", "pattern": "^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$"}, "description": "Explicit owner/name repositories."},
    "topics": {"type": "array", "maxItems": 20, "items": {"type": "string", "maxLength": 50}, "description": "Only import owner repositories carrying one of these topics."},
    "include_forks": {"type": "boolean"},
    "include_archived": {"type": "boolean"},
    "releases": {"type": "integer", "minimum": 0, "maximum": 100, "default": 10, "description": "Latest releases per repository (0 = none)."},
    "deployments": {"type": "boolean", "default": true, "description": "Latest deployment per environment."},
    "pull_requests": {"type": "boolean", "default": true, "description": "Open and recently closed pull requests."},
    "issues": {"type": "boolean", "default": true, "description": "Open and recently closed issues."},
    "closed_retention_days": {"type": "integer", "minimum": 0, "maximum": 90, "default": 14, "description": "How long closed pull requests and issues stay in the catalog."},
    "max_repos": {"type": "integer", "minimum": 1, "maximum": 2000, "default": 200},
    "max_items_per_repo": {"type": "integer", "minimum": 1, "maximum": 1000, "default": 100, "description": "Cap per repository for pull requests and for issues."},
    "min_rate_remaining": {"type": "integer", "minimum": 0, "maximum": 5000, "default": 100, "description": "Stop (as a partial run) when the API rate budget falls below this."},
    "allow_cidrs": {"type": "array", "items": {"type": "string"}, "description": "Private CIDRs a GitHub Enterprise host resolves to."},
    "insecure_http": {"type": "boolean"}
  }
}`

// GitHub is the github provider: repositories, releases, deployments, pull requests and issues through the REST API
// with a fine-grained read-only token (Metadata, Contents, Deployments, Pull requests and Issues: read).
type GitHub struct{}

// Describe implements sources.Provider.
func (GitHub) Describe() sources.Descriptor {
	return sources.Descriptor{Type: GitHubType, Title: "GitHub", Description: "Imports repositories, releases, the latest deployment per " +
		"environment, and open or recently closed pull requests and issues. Pull requests and issues link to catalog entities through " +
		"`deed:<kind>/<name>` (or `deed:ent_…`) references in their body or labels. Use a fine-grained token with read-only Metadata, " +
		"Contents, Deployments, Pull requests and Issues permissions.", DefaultLayer: catalog.LayerImported,
		ConfigSchema: json.RawMessage(githubSchema), SecretFields: []string{"token"}, Capabilities: sources.Capabilities{Pull: true, Snapshot: true},
		Kinds: []sources.KindSupport{{Kind: "repository", Level: sources.SupportNative}, {Kind: "release", Level: sources.SupportNative},
			{Kind: "deployment", Level: sources.SupportNative, Note: "latest per environment"},
			{Kind: "pull_request", Level: sources.SupportNative, Note: "open + recently closed"}, {Kind: "issue", Level: sources.SupportNative, Note: "open + recently closed"}},
		RelTypes: []string{"contains", "deployed_from", "references", "tracks"}, DefaultSchedule: 900, Docs: "docs/CONNECTORS.md#github",
		Verified: "contract tests against fixtures shaped by the GitHub REST API (2022-11-28); real GitHub validation pending",
		// Pull requests and issues churn: closing a burst of them must not trip the mass-removal guard.
		DefaultPolicy: &catalog.Policy{MaxRemovalPercent: 90}}
}

func parseGitHub(raw json.RawMessage) (*GitHubConfig, error) {
	c, err := decode[GitHubConfig](raw, "github")
	if err != nil {
		return nil, err
	}
	if c.APIURL == "" {
		c.APIURL = "https://api.github.com"
	}
	if c.Releases == nil {
		n := 10
		c.Releases = &n
	}
	t := true
	if c.Deployments == nil {
		c.Deployments = &t
	}
	if c.PullRequests == nil {
		c.PullRequests = &t
	}
	if c.Issues == nil {
		c.Issues = &t
	}
	if c.ClosedRetentionDays == 0 {
		c.ClosedRetentionDays = 14
	}
	if c.MaxRepos == 0 {
		c.MaxRepos = 200
	}
	if c.MaxItemsPerRepo == 0 {
		c.MaxItemsPerRepo = 100
	}
	if c.MinRateRemaining == 0 {
		c.MinRateRemaining = 100
	}
	return c, nil
}

// Validate implements sources.Provider.
func (GitHub) Validate(raw json.RawMessage, _ map[string]string) error {
	c, err := parseGitHub(raw)
	if err != nil {
		return err
	}
	if err := validateBaseURL("api_url", c.APIURL, c.InsecureHTTP); err != nil {
		return err
	}
	if len(c.Owners) == 0 && len(c.Repositories) == 0 {
		return apperr.New(apperr.CodeInvalid, "set owners or repositories")
	}
	return nil
}

func githubClient(env sources.Env, c *GitHubConfig) (*apiClient, error) {
	h := map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
	if tok := env.Secrets["token"]; tok != "" {
		h["Authorization"] = "Bearer " + tok
	}
	return newClient(env, "GitHub", c.APIURL, c.AllowCIDRs, c.InsecureHTTP, h)
}

// GitHub REST DTOs (subset).
type ghUser struct {
	Login string `json:"login"`
}

type ghRepo struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	FullName      string     `json:"full_name"`
	HTMLURL       string     `json:"html_url"`
	Description   string     `json:"description"`
	DefaultBranch string     `json:"default_branch"`
	Visibility    string     `json:"visibility"`
	Private       bool       `json:"private"`
	Archived      bool       `json:"archived"`
	Disabled      bool       `json:"disabled"`
	Fork          bool       `json:"fork"`
	Language      string     `json:"language"`
	Topics        []string   `json:"topics"`
	PushedAt      *time.Time `json:"pushed_at"`
	OpenIssues    int64      `json:"open_issues_count"`
	Stars         int64      `json:"stargazers_count"`
	Owner         ghUser     `json:"owner"`
}

type ghRelease struct {
	ID          int64      `json:"id"`
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
}

type ghDeployment struct {
	ID          int64     `json:"id"`
	SHA         string    `json:"sha"`
	Ref         string    `json:"ref"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
}

type ghDeploymentStatus struct {
	State          string    `json:"state"`
	EnvironmentURL string    `json:"environment_url"`
	LogURL         string    `json:"log_url"`
	CreatedAt      time.Time `json:"created_at"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghIssue struct {
	ID          int64           `json:"id"`
	Number      int64           `json:"number"`
	Title       string          `json:"title"`
	State       string          `json:"state"`
	HTMLURL     string          `json:"html_url"`
	Body        string          `json:"body"`
	User        ghUser          `json:"user"`
	Labels      []ghLabel       `json:"labels"`
	Draft       bool            `json:"draft"`
	MergedAt    *time.Time      `json:"merged_at"`
	ClosedAt    *time.Time      `json:"closed_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	PullRequest json.RawMessage `json:"pull_request"`
	Head        *struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base *struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// list follows Link rel="next" pagination up to max items.
func ghList[T any](ctx context.Context, c *apiClient, path string, q url.Values, max int, stop func(T) bool) ([]T, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", strconv.Itoa(min(max, 100)))
	u := c.base + path + "?" + q.Encode()
	var out []T
	for u != "" && len(out) < max {
		var batch []T
		h, err := c.getURL(ctx, u, &batch)
		if err != nil {
			return nil, err
		}
		for _, it := range batch {
			if stop != nil && stop(it) {
				return out, nil
			}
			out = append(out, it)
			if len(out) >= max {
				return out, nil
			}
		}
		u = ""
		if m := linkNext.FindStringSubmatch(h.Get("Link")); m != nil {
			u = m[1]
		}
	}
	return out, nil
}

// Test implements sources.Provider.
func (GitHub) Test(ctx context.Context, env sources.Env) []sources.Diagnostic {
	c, err := parseGitHub(env.Config)
	if err != nil {
		return diagErr("config", err)
	}
	gc, err := githubClient(env, c)
	if err != nil {
		return diagErr("config", err)
	}
	var rl struct {
		Resources struct {
			Core struct {
				Limit     int `json:"limit"`
				Remaining int `json:"remaining"`
			} `json:"core"`
		} `json:"resources"`
	}
	if _, err := gc.get(ctx, "/rate_limit", nil, &rl); err != nil {
		return diagErr("connect", err)
	}
	out := []sources.Diagnostic{{Level: "info", Code: "rate_limit", Message: fmt.Sprintf("API rate budget %d/%d", rl.Resources.Core.Remaining, rl.Resources.Core.Limit)}}
	if env.Secrets["token"] == "" {
		out = append(out, sources.Diagnostic{Level: "warning", Code: "anonymous", Message: "no token: only public repositories, 60 requests per hour"})
	}
	for _, full := range c.Repositories {
		var r ghRepo
		if _, err := gc.get(ctx, "/repos/"+full, nil, &r); err != nil {
			return append(out, sources.Diagnostic{Level: "error", Code: "repository", Message: full + ": " + message(err)})
		}
	}
	for _, o := range c.Owners {
		repos, err := ownerRepos(ctx, gc, o, 1)
		if err != nil {
			return append(out, sources.Diagnostic{Level: "error", Code: "owner", Message: o + ": " + message(err)})
		}
		if len(repos) == 0 {
			out = append(out, sources.Diagnostic{Level: "warning", Code: "owner", Message: o + ": no repositories visible to the token"})
		}
	}
	return append(out, sources.Diagnostic{Level: "info", Code: "ok", Message: "repositories reachable"})
}

func ownerRepos(ctx context.Context, gc *apiClient, owner string, max int) ([]ghRepo, error) {
	if owner == "@me" {
		return ghList[ghRepo](ctx, gc, "/user/repos", url.Values{"affiliation": {"owner"}, "sort": {"full_name"}}, max, nil)
	}
	repos, err := ghList[ghRepo](ctx, gc, "/orgs/"+url.PathEscape(owner)+"/repos", url.Values{"type": {"all"}, "sort": {"full_name"}}, max, nil)
	if errors.Is(err, errNotFound) {
		return ghList[ghRepo](ctx, gc, "/users/"+url.PathEscape(owner)+"/repos", url.Values{"type": {"owner"}, "sort": {"full_name"}}, max, nil)
	}
	return repos, err
}

// Deed references in pull request and issue bodies or labels: deed:<kind>/<name> or deed:ent_<ULID>.
var deedRef = regexp.MustCompile(`(?:^|[\s(\[,;])deed:(ent_[0-9A-HJKMNP-TV-Z]{26}|[a-z][a-z0-9_]{0,62}/[a-z0-9][a-z0-9._-]{0,199})`)

// DeedRefs extracts up to 20 distinct catalog references from free text.
func DeedRefs(texts ...string) []catalog.Ref {
	seen := map[string]bool{}
	var out []catalog.Ref
	for _, t := range texts {
		for _, m := range deedRef.FindAllStringSubmatch(t, -1) {
			v := strings.TrimRight(m[1], ".-_")
			if seen[v] || len(out) >= 20 {
				continue
			}
			seen[v] = true
			if strings.HasPrefix(v, "ent_") {
				out = append(out, catalog.Ref{ID: v})
			} else {
				k, n, _ := strings.Cut(v, "/")
				out = append(out, catalog.Ref{Kind: k, Name: n})
			}
		}
	}
	return out
}

type ghSync struct {
	ctx    context.Context
	c      *GitHubConfig
	gc     *apiClient
	em     sources.Emitter
	now    time.Time
	cutoff time.Time
	short  bool // rate budget exhausted: the run is partial
}

func (s *ghSync) budget() bool {
	if s.gc.remaining >= 0 && s.gc.remaining < s.c.MinRateRemaining {
		if !s.short {
			s.short = true
			s.em.Partial("GitHub rate budget below min_rate_remaining")
			s.em.Diag(sources.Diagnostic{Level: "warning", Code: "rate_limit", Message: fmt.Sprintf("stopped with %d requests left in the rate budget", s.gc.remaining)})
		}
		return false
	}
	return true
}

// soft records a per-repository failure: the run continues but becomes partial so nothing is tombstoned.
func (s *ghSync) soft(code, repo string, err error) {
	s.em.Partial("GitHub " + code + " failed for " + repo)
	s.em.Diag(sources.Diagnostic{Level: "warning", Code: code, Message: repo + ": " + message(err)})
}

// Sync implements sources.Provider.
func (GitHub) Sync(ctx context.Context, env sources.Env, em sources.Emitter) error {
	c, err := parseGitHub(env.Config)
	if err != nil {
		return err
	}
	gc, err := githubClient(env, c)
	if err != nil {
		return err
	}
	now := env.Clock.Now()
	s := &ghSync{ctx: ctx, c: c, gc: gc, em: em, now: now, cutoff: now.Add(-time.Duration(c.ClosedRetentionDays) * 24 * time.Hour)}
	repos := map[int64]ghRepo{}
	var order []int64
	add := func(r ghRepo) {
		if _, ok := repos[r.ID]; !ok && len(order) < c.MaxRepos {
			repos[r.ID] = r
			order = append(order, r.ID)
		}
	}
	for _, full := range c.Repositories {
		var r ghRepo
		if _, err := gc.get(ctx, "/repos/"+full, nil, &r); err != nil {
			if errors.Is(err, errNotFound) {
				em.Diag(sources.Diagnostic{Level: "warning", Code: "repository", Message: full + ": not found or not visible to the token"})
				continue
			}
			return err
		}
		add(r)
	}
	skipped := 0
	for _, o := range c.Owners {
		list, err := ownerRepos(ctx, gc, o, c.MaxRepos)
		if err != nil {
			return err
		}
		for _, r := range list {
			if (r.Fork && !c.IncludeForks) || (r.Archived && !c.IncludeArchived) || r.Disabled || !hasTopic(r.Topics, c.Topics) {
				skipped++
				continue
			}
			add(r)
		}
	}
	if skipped > 0 {
		em.Diag(sources.Diagnostic{Level: "info", Code: "filtered", Message: "repositories skipped by fork/archived/topic filters", Count: skipped})
	}
	if len(order) >= c.MaxRepos {
		em.Partial("max_repos reached")
		em.Diag(sources.Diagnostic{Level: "warning", Code: "max_repos", Message: "repository cap reached; raise max_repos", Count: c.MaxRepos})
	}
	for _, id := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.repo(repos[id]); err != nil {
			return err
		}
	}
	return nil
}

func hasTopic(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		for _, h := range have {
			if strings.EqualFold(h, w) {
				return true
			}
		}
	}
	return false
}

func (s *ghSync) repo(r ghRepo) error {
	ref := catalog.Ref{ExternalID: fmt.Sprintf("repo:%d", r.ID)}
	vis := r.Visibility
	if vis == "" {
		vis = map[bool]string{true: "private", false: "public"}[r.Private]
	}
	md := map[string]any{"displayName": r.FullName, "icon": "simple-icons:github",
		"links": []any{map[string]any{"type": "repository", "title": "GitHub", "url": r.HTMLURL}}}
	if r.Description != "" {
		md["description"] = r.Description
	}
	if len(r.Topics) > 0 {
		md["tags"] = strs(r.Topics)
	}
	if r.Archived {
		md["lifecycle"] = "deprecated"
	}
	spec := withoutNil(map[string]any{"url": r.HTMLURL, "provider": "github", "full_name": r.FullName, "default_branch": r.DefaultBranch,
		"visibility": vis, "archived": r.Archived, "language": r.Language, "fork": r.Fork})
	if err := s.em.Entity(catalog.EntityItem{Kind: "repository", Name: slug(r.FullName), ExternalID: ref.ExternalID, Metadata: md, Spec: spec,
		Observed: withoutNil(map[string]any{"pushed_at": ts(r.PushedAt), "open_issues": r.OpenIssues, "stars": r.Stars})}); err != nil {
		return err
	}
	base := "/repos/" + r.FullName
	tags := map[string]catalog.Ref{}
	if *s.c.Releases > 0 && s.budget() {
		rels, err := ghList[ghRelease](s.ctx, s.gc, base+"/releases", nil, *s.c.Releases, nil)
		if err != nil {
			s.soft("releases", r.FullName, err)
		}
		for _, rl := range rels {
			if rl.Draft {
				continue
			}
			rref := catalog.Ref{ExternalID: fmt.Sprintf("release:%d", rl.ID)}
			tags[rl.TagName] = rref
			name := rl.Name
			if name == "" {
				name = rl.TagName
			}
			if err := s.em.Entity(catalog.EntityItem{Kind: "release", Name: slug(r.FullName + "-" + rl.TagName), ExternalID: rref.ExternalID,
				Metadata: map[string]any{"displayName": r.FullName + " " + rl.TagName, "version": map[string]any{"declared": rl.TagName}, "icon": "simple-icons:github"},
				Spec: withoutNil(map[string]any{"tag": rl.TagName, "name": name, "url": rl.HTMLURL, "published_at": ts(rl.PublishedAt),
					"prerelease": rl.Prerelease, "draft": rl.Draft, "repository": r.FullName})}); err != nil {
				return err
			}
			if err := s.rel("contains", ref, rref, map[string]any{"basis": "github_release"}); err != nil {
				return err
			}
		}
	}
	if *s.c.Deployments && s.budget() {
		if err := s.deployments(r, ref, base, tags); err != nil {
			return err
		}
	}
	if *s.c.PullRequests && s.budget() {
		if err := s.issues(r, ref, base+"/pulls", true); err != nil {
			return err
		}
	}
	if *s.c.Issues && s.budget() {
		if err := s.issues(r, ref, base+"/issues", false); err != nil {
			return err
		}
	}
	return nil
}

func (s *ghSync) rel(typ string, from, to catalog.Ref, ev map[string]any) error {
	return s.em.Relation(catalog.RelationItem{Type: typ, From: from, To: to, Evidence: ev, ExternalID: typ + ":" + from.String() + "->" + to.String()})
}

func (s *ghSync) deployments(r ghRepo, ref catalog.Ref, base string, tags map[string]catalog.Ref) error {
	deps, err := ghList[ghDeployment](s.ctx, s.gc, base+"/deployments", nil, 100, nil)
	if err != nil {
		s.soft("deployments", r.FullName, err)
		return nil
	}
	// The list is newest first: keep the latest deployment per environment.
	latest := map[string]ghDeployment{}
	var envs []string
	for _, d := range deps {
		if _, ok := latest[d.Environment]; !ok && d.Environment != "" {
			latest[d.Environment] = d
			envs = append(envs, d.Environment)
		}
	}
	sort.Strings(envs)
	for _, e := range envs {
		d := latest[e]
		status := ""
		var url string
		if s.budget() {
			sts, err := ghList[ghDeploymentStatus](s.ctx, s.gc, fmt.Sprintf("%s/deployments/%d/statuses", base, d.ID), nil, 1, nil)
			if err != nil {
				s.soft("deployment_statuses", r.FullName, err)
			} else if len(sts) > 0 {
				status, url = sts[0].State, sts[0].EnvironmentURL
			}
		}
		dref := catalog.Ref{ExternalID: "deployment:" + strconv.FormatInt(r.ID, 10) + ":" + e}
		var health *catalog.HealthReport
		switch status {
		case "success", "inactive":
			health = &catalog.HealthReport{State: "ok"}
		case "failure", "error":
			health = &catalog.HealthReport{State: "failed", Reason: "latest GitHub deployment status: " + status}
		case "pending", "queued", "in_progress":
			health = &catalog.HealthReport{State: "warning", Reason: "deployment " + status}
		}
		ct := d.CreatedAt
		if err := s.em.Entity(catalog.EntityItem{Kind: "deployment", Name: slug(r.FullName + "-" + e), ExternalID: dref.ExternalID,
			Metadata: map[string]any{"displayName": r.FullName + " → " + e, "environment": envSlug(e), "icon": "simple-icons:github"},
			Spec: withoutNil(map[string]any{"environment": e, "ref": d.Ref, "sha": d.SHA, "status": status, "url": url, "deployed_at": ts(&ct),
				"repository": r.FullName, "deployment_id": d.ID}), Health: health}); err != nil {
			return err
		}
		if err := s.rel("deployed_from", dref, ref, map[string]any{"basis": "github_deployment", "ref": d.Ref}); err != nil {
			return err
		}
		if tr, ok := tags[d.Ref]; ok {
			if err := s.rel("deployed_from", dref, tr, map[string]any{"basis": "github_deployment_ref_matches_release_tag", "ref": d.Ref}); err != nil {
				return err
			}
		}
	}
	return nil
}

// issues imports open items plus items closed within the retention window (two queries, so long-open items are never
// cut off by the updated-at ordering).
func (s *ghSync) issues(r ghRepo, repoRef catalog.Ref, path string, pulls bool) error {
	kind, code := "issue", "issues"
	if pulls {
		kind, code = "pull_request", "pull_requests"
	}
	open, err := ghList[ghIssue](s.ctx, s.gc, path, url.Values{"state": {"open"}, "sort": {"updated"}, "direction": {"desc"}}, s.c.MaxItemsPerRepo, nil)
	if err != nil {
		s.soft(code, r.FullName, err)
		return nil
	}
	var closed []ghIssue
	if s.c.ClosedRetentionDays > 0 && s.budget() {
		q := url.Values{"state": {"closed"}, "sort": {"updated"}, "direction": {"desc"}}
		if !pulls {
			q.Set("since", s.cutoff.UTC().Format(time.RFC3339))
		}
		closed, err = ghList[ghIssue](s.ctx, s.gc, path, q, s.c.MaxItemsPerRepo, func(it ghIssue) bool { return it.UpdatedAt.Before(s.cutoff) })
		if err != nil {
			s.soft(code, r.FullName, err)
			return nil
		}
	}
	if len(open) >= s.c.MaxItemsPerRepo {
		s.em.Diag(sources.Diagnostic{Level: "warning", Code: code + "_cap", Message: r.FullName + ": open " + code + " exceed max_items_per_repo; older ones are not imported", Count: len(open)})
	}
	for _, it := range append(open, closed...) {
		if !pulls && len(it.PullRequest) > 0 && string(it.PullRequest) != "null" {
			continue // the issues API also lists pull requests
		}
		if it.State == "closed" && (it.ClosedAt == nil || it.ClosedAt.Before(s.cutoff)) {
			continue
		}
		state := it.State
		if pulls && it.MergedAt != nil {
			state = "merged"
		}
		labels := make([]string, 0, len(it.Labels))
		for _, l := range it.Labels {
			labels = append(labels, l.Name)
		}
		title := truncate(it.Title, 300)
		spec := map[string]any{"number": it.Number, "title": title, "state": state, "url": it.HTMLURL, "author": it.User.Login,
			"updated_at": it.UpdatedAt.UTC().Format(time.RFC3339), "repository": r.FullName, "labels": strs(labels)}
		if pulls {
			spec["merged"] = it.MergedAt != nil
			spec["draft"] = it.Draft
			if it.Head != nil && it.Base != nil {
				spec["head"], spec["base"] = it.Head.Ref, it.Base.Ref
			}
		}
		var exp *time.Time
		if it.ClosedAt != nil && it.State == "closed" {
			e := it.ClosedAt.Add(time.Duration(s.c.ClosedRetentionDays) * 24 * time.Hour)
			exp = &e
		}
		prefix := "issue-"
		if pulls {
			prefix = "pr-"
		}
		iref := catalog.Ref{ExternalID: fmt.Sprintf("%s:%d", kind, it.ID)}
		if err := s.em.Entity(catalog.EntityItem{Kind: kind, Name: slug(r.FullName + "-" + prefix + strconv.FormatInt(it.Number, 10)),
			ExternalID: iref.ExternalID, ExpiresAt: exp,
			Metadata: map[string]any{"displayName": fmt.Sprintf("%s#%d %s", r.FullName, it.Number, truncate(title, 120)), "icon": "simple-icons:github",
				"links": []any{map[string]any{"type": "ticket", "title": "GitHub", "url": it.HTMLURL}}},
			Spec: spec}); err != nil {
			return err
		}
		if err := s.em.Relation(catalog.RelationItem{Type: "references", From: iref, To: repoRef, ExpiresAt: exp,
			Evidence: map[string]any{"basis": "github_" + kind}, ExternalID: "references:" + iref.String() + "->" + repoRef.String()}); err != nil {
			return err
		}
		for _, target := range DeedRefs(append([]string{it.Body}, labels...)...) {
			if err := s.em.Relation(catalog.RelationItem{Type: "tracks", From: iref, To: target, ExpiresAt: exp,
				Evidence:   map[string]any{"basis": "deed_reference", "reference": target.String()},
				ExternalID: "tracks:" + iref.String() + "->" + target.String()}); err != nil {
				return err
			}
		}
	}
	return nil
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
