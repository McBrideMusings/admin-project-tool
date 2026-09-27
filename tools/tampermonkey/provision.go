package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// tmHash is Tampermonkey's structural hash of a parsed JSON value, ported
// from its background.js (5.5.0): a leaf hashes as sha256("<typeof>:<value>"),
// an array as the sha256 of its children's hashes concatenated, an object the
// same over its values in sorted-key order with the keys omitted.
func tmHash(v any) string {
	switch x := v.(type) {
	case nil:
		return sha("object:null")
	case bool:
		return sha("boolean:" + strconv.FormatBool(x))
	case json.Number:
		return sha("number:" + jsNumber(x))
	case string:
		return sha("string:" + x)
	case []any:
		var b strings.Builder
		for _, e := range x {
			b.WriteString(tmHash(e))
		}
		return sha(b.String())
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(tmHash(x[k]))
		}
		return sha(b.String())
	default:
		panic(fmt.Sprintf("tmHash: unhashable JSON value %T", v))
	}
}

// jsNumber prints a JSON number the way JavaScript's String(n) does for the
// values provisioning JSON can hold: an integral value without a fraction.
func jsNumber(n json.Number) string {
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// scriptHeader returns the first value of each `// @key value` line inside the
// ==UserScript== block.
func scriptHeader(source string) map[string]string {
	out := map[string]string{}
	inside := false
	for _, line := range strings.Split(source, "\n") {
		s := strings.TrimSpace(line)
		switch {
		case s == "// ==UserScript==":
			inside = true
		case s == "// ==/UserScript==":
			return out
		case inside && strings.HasPrefix(s, "// @"):
			key, val, _ := strings.Cut(s[4:], " ")
			key = strings.TrimSpace(key)
			if _, seen := out[key]; !seen {
				out[key] = strings.TrimSpace(val)
			}
		}
	}
	return out
}

var envNameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// expandEnvPlaceholders expands ${NAME} / ${NAME:-default} for UPPER_CASE names
// only. The script is JavaScript, full of template literals like ${host} that
// must survive untouched. An unset name with no default is an error, so the
// provisioning aborts rather than embed a literal placeholder.
func expandEnvPlaceholders(src string) (string, error) {
	if !strings.Contains(src, "${") {
		return src, nil
	}
	var b strings.Builder
	pos := 0
	for _, ref := range manifest.IterEnvRefs(src) {
		b.WriteString(src[pos:ref.Start])
		raw := src[ref.Start:ref.End]
		if envNameRE.MatchString(ref.Name) {
			v, err := manifest.ResolveEnv(raw)
			if err != nil {
				return "", err
			}
			raw = v
		}
		b.WriteString(raw)
		pos = ref.End
	}
	b.WriteString(src[pos:])
	return b.String(), nil
}

// provisioningScript and provisioningDoc are Tampermonkey's export format. The
// field order is the order Tampermonkey writes it in.
type provisioningScript struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

type provisioningDoc struct {
	Version string               `json:"version"`
	Scripts []provisioningScript `json:"scripts"`
}

// buildProvisioning reads each userscript, expands its environment
// placeholders and embeds it, enabled, in one export document.
func buildProvisioning(paths []string) (provisioningDoc, error) {
	doc := provisioningDoc{Version: "1"}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return doc, err
		}
		src, err := expandEnvPlaceholders(string(raw))
		if err != nil {
			return doc, fmt.Errorf("%s: %w", p, err)
		}
		name, ok := scriptHeader(src)["name"]
		if !ok {
			return doc, fmt.Errorf("%s: no @name in ==UserScript== header", p)
		}
		doc.Scripts = append(doc.Scripts, provisioningScript{
			Name: name, Enabled: true, Source: base64.StdEncoding.EncodeToString([]byte(src)),
		})
	}
	return doc, nil
}

// policyEntry is one jsonImport entry: the document inline as a data: URL
// plus Tampermonkey's hash of it.
type policyEntry struct {
	Hash                   string
	URL                    string
	HaltOnError            bool
	InstallAsSystemScripts bool
}

func newPolicyEntry(doc provisioningDoc) (policyEntry, error) {
	raw, err := compactJSON(doc)
	if err != nil {
		return policyEntry{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return policyEntry{}, err
	}
	return policyEntry{
		Hash: "1:" + tmHash(parsed),
		URL:  "data:application/json;base64," + base64.StdEncoding.EncodeToString(raw),
		// The user can still edit or remove the installed script.
		InstallAsSystemScripts: false,
	}, nil
}

func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// policyPlist renders {jsonImport: [entry]} as an XML property list, keys
// sorted, in the layout Python's plistlib writes.
func policyPlist(e policyEntry) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n\t<key>jsonImport</key>\n\t<array>\n\t\t<dict>\n")
	field := func(key, value string) {
		b.WriteString("\t\t\t<key>" + key + "</key>\n\t\t\t" + value + "\n")
	}
	field("haltOnError", plistBool(e.HaltOnError))
	field("hash", "<string>"+xmlEscape(e.Hash)+"</string>")
	field("installAsSystemScripts", plistBool(e.InstallAsSystemScripts))
	field("url", "<string>"+xmlEscape(e.URL)+"</string>")
	b.WriteString("\t\t</dict>\n\t</array>\n</dict>\n</plist>\n")
	return []byte(b.String())
}

func plistBool(v bool) string {
	if v {
		return "<true/>"
	}
	return "<false/>"
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
