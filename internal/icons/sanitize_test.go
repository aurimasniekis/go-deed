package icons

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeKeepsGeometry(t *testing.T) {
	in := `<?xml version="1.0"?><!-- c --><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" class="x">
	<title>Router</title><defs><linearGradient id="g"><stop offset="0" stop-color="#ff0000"/></linearGradient></defs>
	<path d="M12 2L2 7l10 5 10-5-10-5z" fill="url(#g)" stroke="currentColor" stroke-width="2"/><use href="#g"/></svg>`
	out, mono, err := Sanitize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`viewBox="0 0 24 24"`, `<title>Router</title>`, `fill="url(#g)"`, `stop-color="#ff0000"`, `href="#g"`, `xmlns="http://www.w3.org/2000/svg"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "class=") || strings.Contains(s, "<!--") || mono {
		t.Fatalf("class/comment kept or wrongly monochrome: %s", s)
	}
	_, mono, _ = Sanitize([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0h1" fill="currentColor"/></svg>`))
	if !mono {
		t.Fatal("currentColor icon not monochrome")
	}
}

func TestSanitizeRemovesActiveContent(t *testing.T) {
	attacks := []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><path d="M0 0"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body xmlns="http://www.w3.org/1999/xhtml"><script>alert(1)</script></body></foreignObject></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><path d="M0 0"/></a></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="https://evil.example/x.svg#a"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0" fill="url(https://evil.example/p)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>@import url(https://evil.example/x.css)</style></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path style="fill:url(javascript:alert(1))" d="M0 0"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="data:image/png;base64,AAAA"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><animate attributeName="href" to="javascript:alert(1)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><set attributeName="onmouseover" to="alert(1)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><use href="data:image/svg+xml,&lt;svg onload=alert(1)&gt;"/></svg>`,
	}
	for _, a := range attacks {
		out, _, err := Sanitize([]byte(a))
		if err != nil {
			continue
		}
		low := strings.ToLower(string(out))
		for _, bad := range []string{"script", "onload", "onmouse", "javascript", "foreignobject", "evil.example", "style", "data:", "<a", "animate", "<set", "<image"} {
			if strings.Contains(low, bad) {
				t.Errorf("%q survived in %s (input %s)", bad, out, a)
			}
		}
	}
}

func TestSanitizeRejects(t *testing.T) {
	for _, in := range []string{
		`<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`,
		`<html><body/></html>`,
		`<svg xmlns="http://www.w3.org/2000/svg"></svg><svg xmlns="http://www.w3.org/2000/svg"></svg>`,
		`<?xml-stylesheet href="x.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
		`not xml`,
		strings.Repeat("<g>", 40) + strings.Repeat("</g>", 40),
		`<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat(" ", MaxBytes) + `</svg>`,
	} {
		if _, _, err := Sanitize([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %.60q: %v", in, err)
		}
	}
}

func FuzzSanitize(f *testing.F) {
	f.Add(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0" fill="url(#a)"/></svg>`)
	f.Add(`<svg xmlns="http://www.w3.org/2000/svg" onload="x"><script>1</script></svg>`)
	f.Fuzz(func(t *testing.T, in string) {
		out, _, err := Sanitize([]byte(in))
		if err != nil {
			return
		}
		// Output must itself be accepted and stable, and never contain active content.
		again, _, err := Sanitize(out)
		if err != nil || string(again) != string(out) {
			t.Fatalf("not idempotent: %q -> %q (%v)", out, again, err)
		}
		low := strings.ToLower(string(out))
		for _, bad := range []string{"<script", "javascript:", "foreignobject", " on", "<style"} {
			if strings.Contains(low, bad) && !strings.Contains(low, "<title") {
				t.Fatalf("%q in %q", bad, out)
			}
		}
	})
}
