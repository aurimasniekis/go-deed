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
	if d.Unsupported["data.vsphere_datacenter"] != 0 || d.Unsupported["cloudflare_zone_setting"] != 1 {
		t.Errorf("unsupported %v", d.Unsupported)
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
