package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerCollect(t *testing.T) {
	dir, err := os.MkdirTemp("", "dkr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			_, _ = io.WriteString(w, `{"ID":"abc","Name":"Host-1","ServerVersion":"27.3.1","OperatingSystem":"Debian 12","NCPU":4,"MemTotal":8589934592,"ContainersRunning":1,"Containers":2}`)
		case "/containers/json":
			_, _ = io.WriteString(w, `[{"Id":"c1","Names":["/web"],"Image":"nginx:1.27","ImageID":"sha256:aa","State":"running","Status":"Up 2 hours (unhealthy)",
			  "Labels":{"com.docker.compose.project":"site","com.docker.compose.service":"web","SECRET_TOKEN":"hunter2","org.opencontainers.image.version":"1.27.3"},
			  "Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}],
			  "NetworkSettings":{"Networks":{"site_default":{"NetworkID":"n1","IPAddress":"172.18.0.2"}}}},
			 {"Id":"c2","Names":["/old"],"Image":"redis","State":"exited","Status":"Exited (0) 3 days ago","Labels":{},"Ports":[],"NetworkSettings":{"Networks":{}}}]`)
		case "/networks":
			_, _ = io.WriteString(w, `[{"Name":"site_default","Id":"n1","Driver":"bridge","Scope":"local","IPAM":{"Config":[{"Subnet":"172.18.0.0/16","Gateway":"172.18.0.1"}]}},{"Name":"host","Id":"h","Driver":"host","Scope":"local"}]`)
		default:
			w.WriteHeader(404)
		}
	})}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	d, err := NewDocker("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	s := string(raw)
	if strings.Contains(s, "hunter2") || strings.Contains(s, "SECRET_TOKEN") {
		t.Fatal("unlisted label forwarded")
	}
	names := map[string]map[string]any{}
	for _, e := range b.Entities {
		names[e.Kind+"/"+e.Name] = e.Spec
	}
	for _, want := range []string{"docker_host/host-1", "network/host-1-site_default", "container/host-1-web", "container/host-1-old"} {
		if names[want] == nil {
			t.Errorf("missing %s in %v", want, names)
		}
	}
	if names["network/host-1-host"] != nil {
		t.Error("host network imported")
	}
	web := names["container/host-1-web"]
	if web["image"] != "nginx" || web["image_tag"] != "1.27" || web["compose_project"] != "site" {
		t.Fatalf("web spec: %v", web)
	}
	for _, e := range b.Entities {
		if e.Name == "host-1-web" && (e.Health.State != "failed" || e.Metadata["version"].(map[string]any)["observed"] != "1.27.3") {
			t.Fatalf("web health/version: %+v", e)
		}
		if e.Name == "host-1-old" && e.Health.State != "warning" {
			t.Fatalf("exited container health: %+v", e.Health)
		}
	}
	rels := map[string]bool{}
	for _, r := range b.Relations {
		rels[r.Type+" "+r.From.ExternalID+" "+r.To.ExternalID] = true
	}
	for _, want := range []string{"runs_on container:c1 host:abc", "member_of container:c1 network:n1", "hosted_on network:n1 host:abc"} {
		if !rels[want] {
			t.Errorf("missing relation %s in %v", want, rels)
		}
	}
}
