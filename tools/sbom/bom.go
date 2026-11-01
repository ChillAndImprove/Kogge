// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The part of CycloneDX 1.6 (https://cyclonedx.org/docs/1.6/json/) that the SBOM uses.

type bom struct {
	Schema       string       `json:"$schema"`
	BOMFormat    string       `json:"bomFormat"`
	SpecVersion  string       `json:"specVersion"`
	SerialNumber string       `json:"serialNumber"`
	Version      int          `json:"version"`
	Metadata     metadata     `json:"metadata"`
	Components   []component  `json:"components"`
	Dependencies []dependency `json:"dependencies"`
}

type metadata struct {
	Timestamp  string      `json:"timestamp"`
	Lifecycles []lifecycle `json:"lifecycles"`
	Tools      tools       `json:"tools"`
	Component  component   `json:"component"`
	Supplier   *entity     `json:"supplier,omitempty"`
	Properties []property  `json:"properties,omitempty"`
}

type lifecycle struct {
	Phase string `json:"phase"`
}

type tools struct {
	Components []component `json:"components"`
}

type entity struct {
	Name string `json:"name"`
}

type component struct {
	Type               string      `json:"type"`
	BOMRef             string      `json:"bom-ref,omitempty"`
	Supplier           *entity     `json:"supplier,omitempty"`
	Manufacturer       *entity     `json:"manufacturer,omitempty"`
	Group              string      `json:"group,omitempty"`
	Name               string      `json:"name"`
	Version            string      `json:"version,omitempty"`
	Description        string      `json:"description,omitempty"`
	Hashes             []hash      `json:"hashes,omitempty"`
	Licenses           []licence   `json:"licenses,omitempty"`
	CPE                string      `json:"cpe,omitempty"`
	PURL               string      `json:"purl,omitempty"`
	ExternalReferences []reference `json:"externalReferences,omitempty"`
	Properties         []property  `json:"properties,omitempty"`
}

type hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

// licence is an entry of a component's licences: a licence (by SPDX identifier or by name), or an SPDX
// expression, which must then be the only entry.
type licence struct {
	License    *licenceRef `json:"license,omitempty"`
	Expression string      `json:"expression,omitempty"`
	// Acknowledgement of an expression (a licence has its own): "declared" by the package, or "concluded" from its
	// licence text.
	Acknowledgement string `json:"acknowledgement,omitempty"`
}

type licenceRef struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name,omitempty"`
	Acknowledgement string `json:"acknowledgement,omitempty"`
}

type reference struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Comment string `json:"comment,omitempty"`
}

type property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type dependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn"`
}

// spdxSchema is CycloneDX 1.6's list of SPDX licence identifiers (schema/spdx.schema.json of the specification
// 1.6.2, Apache-2.0). An identifier outside it would make the SBOM invalid; such licences are given by name.
//
//go:embed spdx.schema.json
var spdxSchema []byte

var spdxIDs = sync.OnceValue(func() map[string]string {
	var s struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(spdxSchema, &s); err != nil {
		panic("spdx.schema.json: " + err.Error())
	}
	ids := map[string]string{}
	for _, id := range s.Enum {
		ids[strings.ToLower(id)] = id
	}
	return ids
})

// licences describes a licence as a package or Kogge's licence detection states it: an SPDX identifier (in its
// canonical spelling), an SPDX expression (with AND, OR or WITH), or else a name.
func licences(s, acknowledgement string) []licence {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return nil
	case strings.Contains(s, " AND ") || strings.Contains(s, " OR ") || strings.Contains(s, " WITH "):
		return []licence{{Expression: s, Acknowledgement: acknowledgement}}
	}
	if id, ok := spdxIDs()[strings.ToLower(strings.Trim(s, "()"))]; ok {
		return []licence{{License: &licenceRef{ID: id, Acknowledgement: acknowledgement}}}
	}
	return []licence{{License: &licenceRef{Name: s, Acknowledgement: acknowledgement}}}
}

// purl builds a package URL (https://github.com/package-url/purl-spec), pkg:TYPE/NAMESPACE/NAME@VERSION?QUALIFIERS,
// with its parts percent-encoded. The qualifiers are pairs of key and value, given sorted by key; empty values
// are left out.
func purl(typ, namespace, name, version string, qualifiers ...string) string {
	var b strings.Builder
	b.WriteString("pkg:" + typ + "/")
	if namespace != "" {
		for _, seg := range strings.Split(namespace, "/") {
			b.WriteString(escape(seg) + "/")
		}
	}
	b.WriteString(escape(name))
	if version != "" {
		b.WriteString("@" + escape(version))
	}
	sep := "?"
	for i := 0; i+1 < len(qualifiers); i += 2 {
		if qualifiers[i+1] != "" {
			b.WriteString(sep + qualifiers[i] + "=" + escape(qualifiers[i+1]))
			sep = "&"
		}
	}
	return b.String()
}

// escape percent-encodes everything but the unreserved characters of RFC 3986.
func escape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
