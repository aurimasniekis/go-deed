// Package agent implements deed-agent's collectors: programs that run next to the infrastructure and push facts
// through the source protocol with a source token (pkg/deedclient). The Docker collector reads the Docker Engine API
// (read-only endpoints) and never sends environment variables, commands or labels outside an allowlist.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/pkg/deedclient"
)

// Docker reads a Docker Engine.
type Docker struct {
	http *http.Client
	base string
	// Labels are container label keys forwarded as Deed labels (values only; keys must be valid label keys).
	Labels []string
}

// NewDocker connects to unix:///path or tcp://host:port (tcp only for a TLS-terminating proxy you control).
func NewDocker(endpoint string) (*Docker, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	d := &Docker{Labels: []string{"com.docker.compose.project", "com.docker.compose.service", "org.opencontainers.image.version"}}
	switch u.Scheme {
	case "unix":
		path := u.Path
		d.http = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dl net.Dialer
			return dl.DialContext(ctx, "unix", path)
		}}}
		d.base = "http://docker"
	case "http", "https", "tcp":
		scheme := u.Scheme
		if scheme == "tcp" {
			scheme = "http"
		}
		d.http = &http.Client{Timeout: 30 * time.Second}
		d.base = scheme + "://" + u.Host
	default:
		return nil, fmt.Errorf("unsupported docker endpoint %q", endpoint)
	}
	return d, nil
}

func (d *Docker) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("docker %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

type dockerInfo struct {
	ID                string `json:"ID"`
	Name              string `json:"Name"`
	ServerVersion     string `json:"ServerVersion"`
	OperatingSystem   string `json:"OperatingSystem"`
	OSType            string `json:"OSType"`
	Architecture      string `json:"Architecture"`
	KernelVersion     string `json:"KernelVersion"`
	NCPU              int    `json:"NCPU"`
	MemTotal          int64  `json:"MemTotal"`
	Containers        int    `json:"Containers"`
	ContainersRunning int    `json:"ContainersRunning"`
}

type dockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	Ports   []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	NetworkSettings struct {
		Networks map[string]struct {
			NetworkID string `json:"NetworkID"`
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type dockerNetwork struct {
	Name     string `json:"Name"`
	ID       string `json:"Id"`
	Driver   string `json:"Driver"`
	Scope    string `json:"Scope"`
	Internal bool   `json:"Internal"`
	IPAM     struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
}

var slugRE = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug turns a name into a Deed entity name.
func Slug(s string) string {
	s = slugRE.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-._")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

var labelKeyRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?/)?[a-zA-Z0-9]([a-zA-Z0-9._-]{0,61}[a-zA-Z0-9])?$`)

func splitImage(image string) (repo, tag string) {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}

// Collect returns a complete snapshot of the engine: the host, its containers and networks.
func (d *Docker) Collect(ctx context.Context) (deedclient.Batch, error) {
	var b deedclient.Batch
	var info dockerInfo
	if err := d.get(ctx, "/info", &info); err != nil {
		return b, err
	}
	var containers []dockerContainer
	if err := d.get(ctx, "/containers/json?all=1", &containers); err != nil {
		return b, err
	}
	var networks []dockerNetwork
	if err := d.get(ctx, "/networks", &networks); err != nil {
		return b, err
	}
	host := Slug(info.Name)
	if host == "" {
		host = "docker-host"
	}
	hostRef := deedclient.Ref{ExternalID: "host:" + info.ID}
	b.Entities = append(b.Entities, deedclient.Entity{Kind: "docker_host", Name: host, ExternalID: hostRef.ExternalID,
		Metadata: map[string]any{"displayName": info.Name, "icon": "simple-icons:docker"},
		Spec: map[string]any{"hostname": info.Name, "docker_version": info.ServerVersion, "os": info.OperatingSystem, "architecture": info.Architecture,
			"kernel": info.KernelVersion, "cpu_cores": info.NCPU, "memory_mb": info.MemTotal >> 20},
		Observed: map[string]any{"containers_running": info.ContainersRunning, "containers": info.Containers},
		Health:   &deedclient.Health{State: "ok"}})
	netRefs := map[string]deedclient.Ref{}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	for _, n := range networks {
		if n.Name == "none" || n.Name == "host" {
			continue
		}
		ref := deedclient.Ref{ExternalID: "network:" + n.ID}
		netRefs[n.ID] = ref
		spec := map[string]any{"type": "docker-" + n.Driver, "scope": n.Scope, "internal": n.Internal}
		if len(n.IPAM.Config) > 0 {
			spec["cidr"], spec["gateway"] = n.IPAM.Config[0].Subnet, n.IPAM.Config[0].Gateway
		}
		b.Entities = append(b.Entities, deedclient.Entity{Kind: "network", Name: Slug(host + "-" + n.Name), ExternalID: ref.ExternalID,
			Metadata: map[string]any{"displayName": n.Name + " (" + info.Name + ")", "icon": "simple-icons:docker"}, Spec: spec})
		if n.Scope == "local" {
			b.Relations = append(b.Relations, deedclient.Relation{Type: "hosted_on", From: ref, To: hostRef})
		}
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].ID < containers[j].ID })
	for _, c := range containers {
		name := strings.TrimPrefix(firstOr(c.Names, c.ID[:min(12, len(c.ID))]), "/")
		ref := deedclient.Ref{ExternalID: "container:" + c.ID}
		repo, tag := splitImage(c.Image)
		var ports []any
		for _, p := range c.Ports {
			if p.PublicPort > 0 {
				ports = append(ports, fmt.Sprintf("%s:%d->%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type))
			} else {
				ports = append(ports, fmt.Sprintf("%d/%s", p.PrivatePort, p.Type))
			}
		}
		spec := map[string]any{"image": repo, "image_tag": tag, "image_digest": c.ImageID, "state": c.State}
		if ports != nil {
			spec["ports"] = ports
		}
		labels := map[string]any{}
		for _, k := range d.Labels {
			if v, ok := c.Labels[k]; ok && labelKeyRE.MatchString(k) && len(v) <= 256 {
				labels[k] = v
			}
		}
		if v := c.Labels["com.docker.compose.project"]; v != "" {
			spec["compose_project"] = v
		}
		if v := c.Labels["com.docker.compose.service"]; v != "" {
			spec["compose_service"] = v
		}
		md := map[string]any{"displayName": name, "icon": "simple-icons:docker", "labels": labels}
		if v := c.Labels["org.opencontainers.image.version"]; v != "" {
			md["version"] = map[string]any{"observed": v}
		}
		if v := c.Labels["org.opencontainers.image.source"]; strings.HasPrefix(v, "https://") && len(v) < 512 {
			md["links"] = []any{map[string]any{"type": "repository", "title": "Source", "url": v}}
		}
		health := &deedclient.Health{State: "ok", Reason: c.Status}
		switch c.State {
		case "running":
			if strings.Contains(c.Status, "(unhealthy)") {
				health = &deedclient.Health{State: "failed", Reason: c.Status}
			}
		case "restarting", "dead":
			health = &deedclient.Health{State: "failed", Reason: c.Status}
		case "exited", "paused", "created":
			health = &deedclient.Health{State: "warning", Reason: c.Status}
		}
		b.Entities = append(b.Entities, deedclient.Entity{Kind: "container", Name: Slug(host + "-" + name), ExternalID: ref.ExternalID, Metadata: md,
			Spec: spec, Health: health, Observed: map[string]any{"status": c.Status}})
		b.Relations = append(b.Relations, deedclient.Relation{Type: "runs_on", From: ref, To: hostRef})
		nets := make([]string, 0, len(c.NetworkSettings.Networks))
		for _, n := range c.NetworkSettings.Networks {
			nets = append(nets, n.NetworkID)
		}
		sort.Strings(nets)
		for _, id := range nets {
			if nr, ok := netRefs[id]; ok {
				b.Relations = append(b.Relations, deedclient.Relation{Type: "member_of", From: ref, To: nr})
			}
		}
	}
	return b, nil
}

func firstOr(v []string, def string) string {
	if len(v) > 0 {
		return v[0]
	}
	return def
}
