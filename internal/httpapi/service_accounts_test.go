package httpapi_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestServiceAccounts(t *testing.T) {
	h := newHarness(t)
	owner := h.owner()
	owner.must("POST", "/projects", map[string]any{"name": "home"}, 201)

	sa := owner.must("POST", "/service-accounts", map[string]any{"name": "terraform", "description": "tf-infra", "installation_role": "admin"}, 201)
	saID := sa["id"].(string)
	if sa["kind"] != "service" || sa["name"] != "terraform" || sa["display_name"] != "terraform" || sa["has_password"] != false {
		t.Fatalf("created: %v", sa)
	}
	owner.must("POST", "/service-accounts", map[string]any{"name": "terraform"}, 409)
	owner.must("POST", "/service-accounts", map[string]any{"name": "Bad Name"}, 422)
	owner.must("POST", "/service-accounts", map[string]any{"name": "boss", "installation_role": "owner"}, 422)
	// People and service accounts are listed separately; a service account is found by ID or name.
	if _, users, _ := owner.do("GET", "/users", nil, nil); strings.Contains(fmt.Sprint(users["_raw"]), "terraform") {
		t.Fatalf("users list includes the service account: %v", users)
	}
	if got := owner.must("GET", "/service-accounts/terraform", nil, 200); got["id"] != saID {
		t.Fatalf("by name: %v", got)
	}
	// No password and no owner role.
	owner.must("POST", "/users/"+saID+"/reset-link", nil, 409)
	owner.must("PATCH", "/users/"+saID, map[string]any{"installation_role": "owner"}, 422)

	// A token inherits the service account's installation role and acts as it.
	created := owner.must("POST", "/service-accounts/terraform/tokens", map[string]any{"name": "ci", "expires_in_days": 366}, 201)
	if created["installation_role"] != "admin" || created["user_id"] != saID {
		t.Fatalf("token: %v", created)
	}
	tf := h.client()
	tf.bearer = created["token"].(string)
	if me := tf.must("GET", "/me", nil, 200); me["user_id"] != saID && me["id"] != saID {
		t.Fatalf("me as service account: %v", me)
	}
	tf.must("POST", "/projects", map[string]any{"name": "lab"}, 201)
	// Automation rotates its own credential with an admin token, then revokes the old one.
	rotated := tf.must("POST", "/service-accounts/terraform/tokens", map[string]any{"name": "ci-2"}, 201)
	tf2 := h.client()
	tf2.bearer = rotated["token"].(string)
	tf2.must("DELETE", "/service-accounts/terraform/tokens/"+created["id"].(string), nil, 204)
	if code, _, _ := tf.do("GET", "/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}

	// Project-only service account: a token cannot exceed its role, and membership bounds what it can do.
	owner.must("POST", "/service-accounts", map[string]any{"name": "reader"}, 201)
	owner.must("POST", "/service-accounts/reader/tokens", map[string]any{"name": "x", "installation_role": "auditor"}, 422)
	rt := owner.must("POST", "/service-accounts/reader/tokens", map[string]any{"name": "x"}, 201)
	reader := h.client()
	reader.bearer = rt["token"].(string)
	reader.must("GET", "/projects/home", nil, 404)
	readerID := owner.must("GET", "/service-accounts/reader", nil, 200)["id"].(string)
	owner.must("PUT", "/projects/home/members/"+readerID, map[string]any{"role": "viewer"}, 204)
	reader.must("GET", "/projects/home", nil, 200)
	reader.must("POST", "/service-accounts", map[string]any{"name": "sneaky"}, 403)
	reader.must("POST", "/service-accounts/reader/tokens", map[string]any{"name": "more"}, 403)

	// A personal token without an installation role cannot manage service accounts either.
	personal := owner.token(map[string]any{"name": "mine"})
	personal.must("POST", "/service-accounts/reader/tokens", map[string]any{"name": "more"}, 403)

	// Disabling revokes every token; deleting removes the account.
	owner.must("PATCH", "/service-accounts/terraform", map[string]any{"status": "disabled", "description": "retired"}, 200)
	if code, _, _ := tf2.do("GET", "/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("token of a disabled service account: %d", code)
	}
	owner.must("POST", "/service-accounts/terraform/tokens", map[string]any{"name": "again"}, 409)
	owner.must("DELETE", "/service-accounts/terraform", nil, 204)
	owner.must("GET", "/service-accounts/terraform", nil, 404)
	if list := fmt.Sprint(owner.must("GET", "/service-accounts", nil, 200)["_raw"]); !strings.Contains(list, `"reader"`) || strings.Contains(list, "terraform") {
		t.Fatalf("list: %s", list)
	}
}
