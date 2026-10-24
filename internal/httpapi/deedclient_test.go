package httpapi_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aurimasniekis/go-deed/pkg/deedclient"
)

// TestDeedClientAgainstAPI drives the public SDK against the real API: a complete snapshot, then a smaller one that
// tombstones what disappeared, then incremental items and a heartbeat.
func TestDeedClientAgainstAPI(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)
	src := owner.must("POST", "/sources", map[string]any{"project": "home", "name": "agent", "provider": "push"}, 201)
	id := src["id"].(string)
	tok := owner.must("POST", "/sources/"+id+"/tokens", map[string]any{"name": "agent"}, 201)["token"].(string)
	c, err := deedclient.New(h.srv.URL, tok, id, deedclient.WithBatchSize(3))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	all := deedclient.Batch{Entities: []deedclient.Entity{{Kind: "docker_host", Name: "host-1", ExternalID: "host", Spec: map[string]any{"hostname": "host-1"}}}}
	for i := 0; i < 7; i++ {
		ext := fmt.Sprintf("c%d", i)
		all.Entities = append(all.Entities, deedclient.Entity{Kind: "container", Name: "web-" + ext, ExternalID: ext, Spec: map[string]any{"image": "nginx"}})
		all.Relations = append(all.Relations, deedclient.Relation{Type: "runs_on", From: deedclient.Ref{ExternalID: ext}, To: deedclient.Ref{ExternalID: "host"}})
	}
	res, err := c.Sync(ctx, all, deedclient.CommitOptions{})
	if err != nil || res.Summary.Created != 15 || res.Summary.Errors != 0 {
		t.Fatalf("first sync: %+v %v", res, err)
	}
	all.Entities = all.Entities[:5] // three containers (and their relations) stopped existing
	all.Relations = all.Relations[:4]
	res, err = c.Sync(ctx, all, deedclient.CommitOptions{AllowMassRemoval: true})
	if err != nil || res.Summary.Removed != 6 {
		t.Fatalf("second sync: %+v %v", res, err)
	}
	inc, err := c.Items(ctx, deedclient.Batch{Entities: []deedclient.Entity{{Kind: "container", Name: "web-extra", ExternalID: "extra"}}}, true)
	if err != nil || !inc.Applied || inc.Summary.Created != 1 {
		t.Fatalf("items: %+v %v", inc, err)
	}
	if err := c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	// The token is bound to its source.
	other := owner.must("POST", "/sources", map[string]any{"project": "home", "name": "other", "provider": "push"}, 201)["id"].(string)
	c2, _ := deedclient.New(h.srv.URL, tok, other, deedclient.WithRetries(0))
	if err := c2.Heartbeat(ctx); err == nil {
		t.Fatal("source token wrote into another source")
	}
}
