// Package demo seeds the clearly labelled synthetic project used for demos, screenshots and benchmarks. Every name
// uses example.com and every address the RFC 5737 documentation ranges; the project is flagged synthetic and every
// entity carries the deed.dev/synthetic label.
package demo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/catalog"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Project is the demo project's name.
const Project = "demo-synthetic"

// Result summarizes a seed.
type Result struct {
	ProjectID string `json:"project_id"`
	Entities  int    `json:"entities"`
	Relations int    `json:"relations"`
}

type ent struct {
	kind, name string
	md, spec   map[string]any
}

type rel struct{ typ, fromKind, from, toKind, to string }

func labels(extra map[string]any) map[string]any {
	md := map[string]any{"labels": map[string]any{"deed.dev/synthetic": "true"}}
	for k, v := range extra {
		md[k] = v
	}
	return md
}

func dataset(now time.Time, scale int) ([]ent, []rel) {
	e := func(kind, name string, md, spec map[string]any) ent {
		return ent{kind: kind, name: name, md: labels(md), spec: spec}
	}
	ents := []ent{
		e("group", "team-platform", map[string]any{"displayName": "Platform team"}, nil),
		e("location", "home-rack", map[string]any{"displayName": "Home rack (synthetic)"}, nil),
		e("environment", "production", nil, nil),
		e("hypervisor", "pve-1", map[string]any{"displayName": "Proxmox node 1", "icon": "thesvg:proxmox", "environment": "production",
			"version": map[string]any{"declared": "8.2.4", "product": "proxmox-ve"}}, map[string]any{"hostname": "pve-1.example.com", "ipv4": "192.0.2.10"}),
		e("server", "nas-1", map[string]any{"displayName": "NAS", "environment": "production"}, map[string]any{"hostname": "nas-1.example.com", "ipv4": "192.0.2.20"}),
		e("vm", "vm-web-1", map[string]any{"environment": "production"}, map[string]any{"hostname": "vm-web-1.example.com", "ipv4": "192.0.2.31"}),
		e("vm", "vm-db-1", map[string]any{"environment": "production"}, map[string]any{"hostname": "vm-db-1.example.com", "ipv4": "192.0.2.32"}),
		e("vm", "vm-docker-1", map[string]any{"environment": "production"}, map[string]any{"hostname": "vm-docker-1.example.com", "ipv4": "192.0.2.33"}),
		e("docker_host", "docker-1", map[string]any{"icon": "simple-icons:docker"}, map[string]any{"hostname": "vm-docker-1.example.com", "docker_version": "27.3.1"}),
		e("database", "postgres-main", map[string]any{"icon": "simple-icons:postgresql", "owner": "team-platform",
			"version": map[string]any{"declared": "16.2", "product": "postgresql"}}, map[string]any{"engine": "postgresql"}),
		e("domain", "example.com", nil, map[string]any{"fqdn": "example.com", "registrar": "Example Registrar",
			"expires_at": now.AddDate(0, 0, 200).Format(time.RFC3339)}),
		e("dns_zone", "example.com", map[string]any{"icon": "simple-icons:cloudflare"}, map[string]any{"zone": "example.com", "provider": "cloudflare"}),
		e("certificate", "wildcard-example-com", map[string]any{"displayName": "*.example.com"}, map[string]any{"common_name": "*.example.com",
			"not_after": now.AddDate(0, 0, 20).Format(time.RFC3339), "issuer": "Example CA"}),
		e("nats_system", "pulse", map[string]any{"displayName": "Pulse", "icon": "simple-icons:natsdotio"}, map[string]any{"jetstream": true}),
		e("nats_subject", "evt.shop.order.created", nil, map[string]any{"subject": "evt.shop.order.created"}),
		e("event_contract", "shop.order.created.v1", nil, map[string]any{"type": "shop.order.created.v1"}),
		e("repository", "example-website", map[string]any{"icon": "simple-icons:github"}, map[string]any{"full_name": "example/website",
			"url": "https://github.com/example/website", "provider": "github", "default_branch": "main"}),
		e("release", "example-website-v1.4.0", nil, map[string]any{"tag": "v1.4.0", "published_at": now.AddDate(0, 0, -10).Format(time.RFC3339)}),
		e("release", "example-website-v1.5.0", nil, map[string]any{"tag": "v1.5.0", "published_at": now.AddDate(0, 0, -1).Format(time.RFC3339)}),
		e("grafana_dashboard", "node-overview", map[string]any{"icon": "simple-icons:grafana"}, map[string]any{"uid": "node-overview"}),
	}
	services := []struct{ name, display, icon, host, ver, product string }{
		{"porch", "Porch (identity)", "tabler:key", "id.example.com", "", ""},
		{"hallpass", "HallPass", "tabler:lock-access", "access.example.com", "", ""},
		{"deed", "Deed", "tabler:topology-star-3", "deed.example.com", "", ""},
		{"grafana", "Grafana", "simple-icons:grafana", "grafana.example.com", "11.2.0", "grafana"},
		{"prometheus", "Prometheus", "simple-icons:prometheus", "prometheus.example.com", "2.53.0", "prometheus"},
		{"website", "Website", "tabler:world", "www.example.com", "v1.4.0", ""},
		{"shop-api", "Shop API", "tabler:api", "api.example.com", "", ""},
		{"notifier", "Notifier", "tabler:bell", "", "", ""},
	}
	var rels []rel
	r := func(typ, fk, f, tk, t string) { rels = append(rels, rel{typ, fk, f, tk, t}) }
	for _, s := range services {
		md := map[string]any{"displayName": s.display, "icon": s.icon, "owner": "team-platform", "environment": "production"}
		if s.ver != "" {
			md["version"] = map[string]any{"declared": s.ver, "product": s.product}
		}
		spec := map[string]any{}
		if s.host != "" {
			spec["url"] = "https://" + s.host
			ents = append(ents, e("dns_record", s.host, nil, map[string]any{"fqdn": s.host, "type": "A", "content": "192.0.2.40", "zone": "example.com", "proxied": true}))
			r("resolves_to", "dns_record", s.host, "vm", "vm-web-1")
			r("contains", "dns_zone", "example.com", "dns_record", s.host)
			r("routes_to", "dns_record", s.host, "service", s.name)
			r("secured_by_certificate", "service", s.name, "certificate", "wildcard-example-com")
		}
		ents = append(ents, e("service", s.name, md, spec))
		ents = append(ents, e("container", "docker-1-"+s.name, map[string]any{"icon": "simple-icons:docker"}, map[string]any{"image": "example/" + s.name, "state": "running"}))
		r("runs_on", "service", s.name, "container", "docker-1-"+s.name)
		r("runs_on", "container", "docker-1-"+s.name, "docker_host", "docker-1")
		r("owned_by", "service", s.name, "group", "team-platform")
		r("monitored_by", "service", s.name, "grafana_dashboard", "node-overview")
	}
	r("runs_on", "vm", "vm-web-1", "hypervisor", "pve-1")
	r("runs_on", "vm", "vm-db-1", "hypervisor", "pve-1")
	r("runs_on", "vm", "vm-docker-1", "hypervisor", "pve-1")
	r("runs_on", "docker_host", "docker-1", "vm", "vm-docker-1")
	r("runs_on", "database", "postgres-main", "vm", "vm-db-1")
	r("located_in", "hypervisor", "pve-1", "location", "home-rack")
	r("located_in", "server", "nas-1", "location", "home-rack")
	r("stored_in", "database", "postgres-main", "server", "nas-1")
	r("depends_on", "service", "shop-api", "database", "postgres-main")
	r("depends_on", "service", "website", "service", "shop-api")
	r("authenticates_with", "service", "grafana", "service", "porch")
	r("authenticates_with", "service", "deed", "service", "porch")
	r("protected_by", "vm", "vm-db-1", "service", "hallpass")
	r("contains", "domain", "example.com", "dns_zone", "example.com")
	r("contains", "nats_system", "pulse", "nats_subject", "evt.shop.order.created")
	r("publishes", "service", "shop-api", "nats_subject", "evt.shop.order.created")
	r("subscribes_to", "service", "notifier", "nats_subject", "evt.shop.order.created")
	r("carried_on", "event_contract", "shop.order.created.v1", "nats_subject", "evt.shop.order.created")
	r("contains", "repository", "example-website", "release", "example-website-v1.4.0")
	r("contains", "repository", "example-website", "release", "example-website-v1.5.0")
	r("deployed_from", "service", "website", "repository", "example-website")
	// Optional scale-out: racks of synthetic hosts with workloads, for benchmarks and load tests. Each workload also
	// depends on up to five workloads on other hosts, so -scale 2500 gives ~10k entities and ~55k relations (the
	// 10k/50k size the performance budgets are stated for).
	cross := map[string]bool{}
	for i := 0; i < scale; i++ {
		h := fmt.Sprintf("host-%04d", i)
		ents = append(ents, e("server", h, map[string]any{"environment": "production"},
			map[string]any{"hostname": h + ".example.com", "ipv4": fmt.Sprintf("198.51.%d.%d", 100+i/250%10, 1+i%250)}))
		r("located_in", "server", h, "location", "home-rack")
		for j := 0; j < 3; j++ {
			w := fmt.Sprintf("%s-app-%d", h, j)
			ents = append(ents, e("container", w, nil, map[string]any{"image": "example/app", "state": "running"}))
			r("runs_on", "container", w, "server", h)
			r("depends_on", "container", w, "database", "postgres-main")
			for k := 1; k <= 5; k++ {
				t := (i + k*37) % scale
				to := fmt.Sprintf("host-%04d-app-%d", t, (j+k)%3)
				if t == i || cross[w+">"+to] {
					continue
				}
				cross[w+">"+to] = true
				r("depends_on", "container", w, "container", to)
			}
		}
	}
	return ents, rels
}

// Seed creates (or refreshes) the synthetic project. It is idempotent: re-running reports unchanged items.
func Seed(ctx context.Context, cat *catalog.Service, st *store.Store, now time.Time, scale int) (*Result, error) {
	p, err := cat.Project(ctx, Project)
	if err != nil {
		if apperr.CodeOf(err) != apperr.CodeNotFound {
			return nil, err
		}
		if p, err = cat.CreateProject(ctx, audit.CLI, catalog.ProjectInput{Name: Project, DisplayName: "Demo (synthetic)", Synthetic: true,
			Description: "Synthetic demo data generated by `deed demo seed`. Not real infrastructure: names use example.com and addresses RFC 5737 ranges."}, ""); err != nil {
			return nil, err
		}
	}
	if !p.Synthetic {
		return nil, errors.New("project demo-synthetic exists but is not flagged synthetic; refusing to write into it")
	}
	src, err := cat.SourceByName(ctx, p.ID, "api")
	if err != nil {
		return nil, err
	}
	ents, rels := dataset(now, scale)
	res := &Result{ProjectID: p.ID}
	for i := 0; i < len(ents); i += 50 {
		chunk := ents[i:min(i+50, len(ents))]
		if err := st.W().TxNamed(ctx, "demo.entities", func(tx *store.Tx) error {
			for _, x := range chunk {
				it := &catalog.EntityItem{Kind: x.kind, Name: x.name, Metadata: x.md, Spec: x.spec}
				if _, err := cat.ApplyEntity(ctx, tx, src, it, catalog.ApplyOpts{}); err != nil {
					return fmt.Errorf("%s/%s: %w", x.kind, x.name, err)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		res.Entities += len(chunk)
	}
	for i := 0; i < len(rels); i += 50 {
		chunk := rels[i:min(i+50, len(rels))]
		if err := st.W().TxNamed(ctx, "demo.relations", func(tx *store.Tx) error {
			for _, x := range chunk {
				it := &catalog.RelationItem{Type: x.typ, From: catalog.Ref{Kind: x.fromKind, Name: x.from}, To: catalog.Ref{Kind: x.toKind, Name: x.to}}
				if _, err := cat.ApplyRelation(ctx, tx, src, it, catalog.ApplyOpts{}); err != nil {
					return fmt.Errorf("%s %s → %s: %w", x.typ, x.from, x.to, err)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		res.Relations += len(chunk)
	}
	return res, nil
}
