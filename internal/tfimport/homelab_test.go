package tfimport

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// TestExtractHomelabProviders covers the vSphere (standalone ESXi), Cloudflare v5, UniFi, GitHub Pages and HallPass
// mappings with a synthetic state: data sources named by a mapping become entities, unmapped ones are not reported as
// unsupported, joins produce the relationships, and secrets never leave the parser.
func TestExtractHomelabProviders(t *testing.T) {
	st := load(t, "homelab-v4.json")
	b, d := Extract(context.Background(), st, MustBuiltin(), Options{})
	out, _ := json.Marshal(b)
	diag, _ := json.Marshal(d)
	for _, blob := range [][]byte{out, diag} {
		for _, secret := range []string{"PLANTED-SECRET", "PLANTED-CSR", "BEGIN CERTIFICATE", "PLANTED-PUBLIC-KEY"} {
			if bytes.Contains(blob, []byte(secret)) {
				t.Fatalf("%s leaked: %s", secret, blob)
			}
		}
	}
	// Zone settings only derive their zone; this state looks the zone up, so nothing is derived.
	if len(d.Unsupported) != 0 || d.Mapped["cloudflare_zone_setting"] != 1 || d.Mapped["derived.cloudflare_zone"] != 0 {
		t.Errorf("unsupported %v, mapped %v", d.Unsupported, d.Mapped)
	}
	if d.Mapped["data.vsphere_host"] != 1 || d.Mapped["data.cloudflare_zone"] != 1 {
		t.Errorf("mapped %v", d.Mapped)
	}
	kinds := map[string]string{}
	for _, e := range b.Entities {
		kinds[e.Name] = e.Kind
	}
	for name, kind := range map[string]string{"esxi-01.example.com": "hypervisor", "ssd-1": "storage", "vswitch0-ha-host": "network_device",
		"servers-ha-host": "network", "app-1": "vm", "example.com": "dns_zone", "custom-waf": "firewall_rule", "origin-origin": "certificate",
		"servers": "network", "wlan-example-home": "network", "nas": "network_client", "gateway": "network_device",
		"forward-https-to-nas": "firewall_rule", "servers-to-internet": "firewall_rule", "peer-laptop": "network_client", "vpn-wireguard": "service",
		"docs-pages": "application", "app-1-admin": "hallpass_role"} {
		if kinds[name] != kind {
			t.Errorf("%s: kind %q, want %q (have %v)", name, kinds[name], kind, kinds)
		}
	}
	rels := map[string]bool{}
	for _, r := range b.Relations {
		rels[r.Type+" "+r.From.ExternalID+" "+r.To.ExternalID] = true
	}
	for _, want := range []string{
		"runs_on vsphere_virtual_machine.app data.vsphere_host.esxi",
		"stored_in vsphere_virtual_machine.app vsphere_vmfs_datastore.ssd",
		"hosted_on vsphere_vmfs_datastore.ssd data.vsphere_host.esxi",
		"hosted_on vsphere_host_virtual_switch.vs0 data.vsphere_host.esxi",
		"contains vsphere_host_virtual_switch.vs0 vsphere_host_port_group.servers",
		"contains data.cloudflare_zone.main cloudflare_dns_record.app",
		"resolves_to cloudflare_dns_record.app vsphere_virtual_machine.app",
		"protected_by data.cloudflare_zone.main cloudflare_ruleset.waf",
		"secured_by_certificate cloudflare_dns_record.app cloudflare_origin_ca_certificate.origin",
		"connects_to unifi_wlan.home unifi_network.servers",
		"connects_to unifi_client.nas unifi_network.servers",
		"routes_to unifi_port_forward.https unifi_client.nas",
		"references unifi_firewall_policy.iot unifi_network.servers",
		"connects_to unifi_wireguard_peer.laptop unifi_vpn_server.wg",
		"deployed_from github_repository_pages.docs github_repository.docs",
		"resolves_to cloudflare_dns_record.app github_repository_pages.docs",
		"protected_by vsphere_virtual_machine.app hallpass_server.app",
		"contains hallpass_server.app hallpass_role.admin",
	} {
		if !rels[want] {
			t.Errorf("missing relationship %s", want)
		}
	}
	if t.Failed() {
		for r := range rels {
			t.Log(r)
		}
	}
}

// TestDerivedZonesAndDomains: a zone module passes only zone IDs to its records, so the zone is derived from them
// (named after the records' registrable domain, public-suffix aware); a zone the state looks up wins over the derived
// one; zones and Google Workspace domains are contained by the hand-declared domain entity with the same FQDN.
func TestDerivedZonesAndDomains(t *testing.T) {
	st := load(t, "zones-module-v4.json")
	domains := map[string]string{"example.dev": "ent_dom_dev", "example.com": "ent_dom_com", "example.co.uk": "ent_dom_uk", "example.org": "ent_dom_org", "example.net": "ent_dom_net"}
	lookup := func(_ context.Context, kinds []string, pointer, value string) (string, bool) {
		if pointer != "/spec/fqdn" || len(kinds) != 1 || kinds[0] != "domain" {
			return "", false
		}
		id, ok := domains[value]
		return id, ok
	}
	b, d := Extract(context.Background(), st, MustBuiltin(), Options{Lookup: lookup})
	// example.dev has no records, only zone settings and origin pulls in module.zone["example.dev"]: named by the module
	// key. The account-level ruleset has no zone and derives nothing.
	if d.Mapped["derived.cloudflare_zone"] != 3 || d.Resources != 10 || len(d.Unsupported) != 0 {
		t.Errorf("mapped %v, %d resources", d.Mapped, d.Resources)
	}
	zones := map[string]string{}
	for _, e := range b.Entities {
		if e.Kind == "dns_zone" {
			zones[e.Name] = e.ExternalID
		}
	}
	want := map[string]string{"example.com": `derived.cloudflare_zone["zone-example-com"]`, "example.co.uk": `derived.cloudflare_zone["zone-example-co-uk"]`,
		"example.org": "data.cloudflare_zone.org", "example.dev": `derived.cloudflare_zone["zone-example-dev"]`}
	if len(zones) != len(want) {
		t.Errorf("zones %v", zones)
	}
	for n, ext := range want {
		if zones[n] != ext {
			t.Errorf("zone %s: %q, want %q", n, zones[n], ext)
		}
	}
	rels := map[string]bool{}
	for _, r := range b.Relations {
		rels[r.Type+" "+r.From.String()+" "+r.To.String()] = true
	}
	for _, w := range []string{
		`contains external:derived.cloudflare_zone["zone-example-com"] external:module.zone["example.com"].cloudflare_dns_record.this["www"]`,
		`contains external:derived.cloudflare_zone["zone-example-com"] external:module.zone["example.com"].cloudflare_dns_record.this["apex"]`,
		`contains external:derived.cloudflare_zone["zone-example-co-uk"] external:module.zone["example.co.uk"].cloudflare_dns_record.this["mail"]`,
		`contains external:data.cloudflare_zone.org external:cloudflare_dns_record.org`,
		`contains ent_dom_com external:derived.cloudflare_zone["zone-example-com"]`,
		`contains ent_dom_uk external:derived.cloudflare_zone["zone-example-co-uk"]`,
		`contains ent_dom_org external:data.cloudflare_zone.org`,
		`contains ent_dom_com external:googleworkspace_domain.primary`,
		`contains ent_dom_net external:googleworkspace_domain_alias.alias`,
		`contains ent_dom_dev external:derived.cloudflare_zone["zone-example-dev"]`,
	} {
		if !rels[w] {
			t.Errorf("missing %s", w)
		}
	}
	if t.Failed() {
		for r := range rels {
			t.Log(r)
		}
	}
}
