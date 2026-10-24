// Package icons stores admin-uploaded SVG icons (ADR-011b). Uploads are parsed and re-serialized through an element
// and attribute allowlist: no scripts, foreignObject, event handlers, styles, external references, entities or
// DOCTYPEs survive. Icons are served with a sandboxing CSP and rendered by the UI through <img> only.
package icons

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// MaxBytes bounds an uploaded SVG.
const MaxBytes = 64 << 10

const (
	maxDepth    = 32
	maxElements = 5000
)

var allowedElements = map[string]bool{
	"svg": true, "g": true, "path": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "rect": true,
	"title": true, "desc": true, "defs": true, "linearGradient": true, "radialGradient": true, "stop": true, "clipPath": true, "mask": true,
	"use": true, "symbol": true,
}

var allowedAttrs = map[string]bool{
	"id": true, "d": true, "fill": true, "fill-rule": true, "clip-rule": true, "fill-opacity": true, "stroke": true, "stroke-width": true,
	"stroke-linecap": true, "stroke-linejoin": true, "stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"stroke-opacity": true, "opacity": true, "transform": true, "viewBox": true, "width": true, "height": true, "x": true, "y": true,
	"x1": true, "y1": true, "x2": true, "y2": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true, "fx": true, "fy": true,
	"points": true, "offset": true, "stop-color": true, "stop-opacity": true, "gradientUnits": true, "gradientTransform": true,
	"spreadMethod": true, "clip-path": true, "mask": true, "maskUnits": true, "clipPathUnits": true, "preserveAspectRatio": true,
	"version": true, "href": true, "visibility": true, "display": true,
}

var (
	// Values are short and made of characters SVG geometry and colors need; url() may only point inside the document.
	valueRE   = regexp.MustCompile(`^[A-Za-z0-9 \t\n\r.,:;%#()+\-_/]*$`)
	urlRE     = regexp.MustCompile(`url\(\s*([^)]*)\)`)
	localRef  = regexp.MustCompile(`^#[A-Za-z][A-Za-z0-9_.:-]{0,127}$`)
	idRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,127}$`)
	colorAttr = map[string]bool{"fill": true, "stroke": true, "stop-color": true}
	monoColor = map[string]bool{"": true, "none": true, "currentcolor": true, "#000": true, "#000000": true, "black": true, "inherit": true}
)

// ErrInvalid is returned for SVGs that cannot be made safe or are not SVG.
var ErrInvalid = errors.New("icons: not an acceptable SVG")

// Sanitize parses an SVG and returns a safe re-serialization and whether it is monochrome (only none, black or
// currentColor paint, so the UI may tint it with a CSS mask).
func Sanitize(in []byte) ([]byte, bool, error) {
	if len(in) > MaxBytes {
		return nil, false, fmt.Errorf("%w: larger than %d bytes", ErrInvalid, MaxBytes)
	}
	dec := xml.NewDecoder(bytes.NewReader(in))
	dec.Strict = true
	dec.Entity = map[string]string{}
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	depth, elements, skip := 0, 0, 0
	rootSeen, mono := false, true
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, false, fmt.Errorf("%w: DOCTYPE and other directives are not allowed", ErrInvalid)
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, false, fmt.Errorf("%w: processing instructions are not allowed", ErrInvalid)
			}
		case xml.Comment:
			// dropped
		case xml.StartElement:
			depth++
			elements++
			if depth > maxDepth || elements > maxElements {
				return nil, false, fmt.Errorf("%w: too deep or too many elements", ErrInvalid)
			}
			if skip > 0 {
				skip++
				continue
			}
			name := t.Name.Local
			if !rootSeen {
				if name != "svg" || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
					return nil, false, fmt.Errorf("%w: the root element must be <svg>", ErrInvalid)
				}
				rootSeen = true
			} else if depth == 1 {
				return nil, false, fmt.Errorf("%w: a single root element is required", ErrInvalid)
			}
			if !allowedElements[name] || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
				// Unknown elements (script, foreignObject, style, image, a, animate…) are dropped with their content.
				skip = 1
				continue
			}
			se := xml.StartElement{Name: xml.Name{Local: name}}
			if name == "svg" && depth == 1 {
				se.Attr = append(se.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"})
			}
			for _, a := range t.Attr {
				an := a.Name.Local
				if a.Name.Space == "http://www.w3.org/1999/xlink" && an == "href" {
					an = "href"
				} else if a.Name.Space != "" || an == "xmlns" {
					continue
				}
				if !allowedAttrs[an] {
					continue // on*, style, class, xml:base, …
				}
				v := strings.TrimSpace(a.Value)
				if !safeValue(an, v) {
					continue
				}
				if colorAttr[an] && !monoColor[strings.ToLower(v)] {
					mono = false
				}
				se.Attr = append(se.Attr, xml.Attr{Name: xml.Name{Local: an}, Value: v})
			}
			if err := enc.EncodeToken(se); err != nil {
				return nil, false, err
			}
		case xml.EndElement:
			depth--
			if skip > 0 {
				skip--
				continue
			}
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return nil, false, err
			}
		case xml.CharData:
			if skip > 0 || depth == 0 {
				continue
			}
			// Text only matters inside <title>/<desc>; whitespace elsewhere is dropped.
			if s := strings.TrimSpace(string(t)); s != "" {
				if err := enc.EncodeToken(xml.CharData(s)); err != nil {
					return nil, false, err
				}
			}
		}
	}
	if !rootSeen {
		return nil, false, fmt.Errorf("%w: no <svg> element", ErrInvalid)
	}
	if err := enc.Flush(); err != nil {
		return nil, false, err
	}
	return out.Bytes(), mono, nil
}

func safeValue(attr, v string) bool {
	if len(v) > 16<<10 {
		return false
	}
	switch attr {
	case "href":
		return localRef.MatchString(v)
	case "id":
		return idRE.MatchString(v)
	}
	if !valueRE.MatchString(v) {
		return false
	}
	low := strings.ToLower(v)
	if strings.Contains(low, "javascript") || strings.Contains(low, "expression") || strings.Contains(low, "data:") {
		return false
	}
	for _, m := range urlRE.FindAllStringSubmatch(v, -1) {
		if !localRef.MatchString(strings.Trim(strings.TrimSpace(m[1]), `'"`)) {
			return false
		}
	}
	return true
}
