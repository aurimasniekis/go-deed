package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// runImportTerraform uploads a state file to a terraform_state source. The state is streamed from disk or stdin
// straight to the server and never printed.
func runImportTerraform(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("import terraform", stderr)
	endpoint := fs.String("endpoint", os.Getenv("DEED_ENDPOINT"), "Deed base URL (default $DEED_ENDPOINT)")
	source := fs.String("source", "", "source ID (src_…) of a terraform_state source")
	file := fs.String("file", "-", "state file (terraform.tfstate or `terraform show -json` output); - reads stdin")
	dryRun := fs.Bool("dry-run", false, "preview without writing")
	partial := fs.Bool("partial", false, "upsert only; never remove resources missing from this state")
	allowMass := fs.Bool("allow-mass-removal", false, "allow a commit that removes more than the source's guard")
	caFile := fs.String("ca-file", os.Getenv("DEED_CA_FILE"), "extra CA bundle (PEM)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	token := os.Getenv("DEED_SOURCE_TOKEN")
	if token == "" {
		token = os.Getenv("DEED_TOKEN")
	}
	if *endpoint == "" || *source == "" || token == "" {
		return &exitError{code: 2, err: errors.New("--endpoint, --source and $DEED_SOURCE_TOKEN (or $DEED_TOKEN) are required")}
	}
	var body io.ReadCloser = os.Stdin
	if *file != "-" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		body = f
	}
	defer body.Close()
	q := url.Values{}
	if *dryRun {
		q.Set("dry_run", "true")
	}
	if *partial {
		q.Set("partial", "true")
	}
	if *allowMass {
		q.Set("allow_mass_removal", "true")
	}
	u := strings.TrimSuffix(*endpoint, "/") + "/api/v1/sources/" + url.PathEscape(*source) + "/terraform/state"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "deed-cli")
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			return err
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(pem)
		tc.RootCAs = pool
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tc, Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var out struct {
		Result *struct {
			DryRun  bool           `json:"dry_run"`
			Summary map[string]int `json:"summary"`
		} `json:"result"`
		Diagnostics []struct {
			Level, Code, Message string
			Count                int
		} `json:"diagnostics"`
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(data, &out)
	if resp.StatusCode != http.StatusOK {
		return &exitError{code: 1, err: fmt.Errorf("%s: %s (%s)", resp.Status, out.Detail, out.Code)}
	}
	mode := "imported"
	if out.Result != nil && out.Result.DryRun {
		mode = "dry run"
	}
	if out.Result != nil {
		s := out.Result.Summary
		_, _ = fmt.Fprintf(stdout, "%s: %d created, %d updated, %d unchanged, %d removed, %d errors, %d unresolved\n", mode, s["created"], s["updated"],
			s["unchanged"], s["removed"], s["errors"], s["unresolved"])
	}
	for _, d := range out.Diagnostics {
		suffix := ""
		if d.Count > 1 {
			suffix = fmt.Sprintf(" (×%d)", d.Count)
		}
		_, _ = fmt.Fprintf(stdout, "  [%s] %s%s\n", d.Level, d.Message, suffix)
	}
	return nil
}
