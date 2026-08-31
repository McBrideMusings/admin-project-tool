package luahost

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"

	lua "github.com/yuin/gopher-lua"
)

// jsonEncode implements the json.encode(value) Lua function: converts a Lua
// table/string/number/bool/nil to its JSON string, using encoding/json —
// this is the capability ADR-0010 calls out ("Bind capability from Go …
// rather than expecting it from Lua's stdlib") in place of hand-rolling a
// JSON encoder in Lua.
func jsonEncode(L *lua.LState) int {
	v := fromLuaValue(L.CheckAny(1))
	b, err := json.Marshal(v)
	if err != nil {
		L.RaiseError("json.encode: %v", err)
		return 0
	}
	L.Push(lua.LString(b))
	return 1
}

// jsonDecode implements json.decode(text): parses a JSON string into the
// equivalent Lua table/string/number/bool/nil via toLuaValue.
func jsonDecode(L *lua.LState) int {
	text := L.CheckString(1)
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		L.RaiseError("json.decode: %v", err)
		return 0
	}
	L.Push(toLuaValue(L, v))
	return 1
}

// envGet implements env(name): reads a process environment variable,
// returning nil when unset. A Lua body gets neither `os` nor `io` (this
// package's doc comment), so this is the one narrow, read-only capability
// bound from Go in their place — most bodies never need it at all, since
// embedding "${VAR}" directly in a string passed to run_cmd lets /bin/sh -c
// resolve it without Lua ever seeing the value; this exists for the smaller
// set of bodies (an archetype's own deploy-target dispatch, e.g.) that
// branch in Lua on whether a var is set, the way the Python original's
// os.environ.get(...) let a body check without spawning a shell.
func envGet(L *lua.LState) int {
	name := L.CheckString(1)
	v, ok := os.LookupEnv(name)
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(lua.LString(v))
	return 1
}

// globMatch implements glob.match(pattern[, dir]): filepath.Glob rooted at
// dir (default "."), returned as a Lua array of matched paths. Bound from Go
// rather than hand-rolled in Lua, per ADR-0010.
func globMatch(L *lua.LState) int {
	pattern := L.CheckString(1)
	dir := L.OptString(2, ".")
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		L.RaiseError("glob.match: %v", err)
		return 0
	}
	tbl := L.NewTable()
	for i, m := range matches {
		tbl.RawSetInt(i+1, lua.LString(m))
	}
	L.Push(tbl)
	return 1
}

// fsRead implements fs.read(path): reads a whole text file as a string, or
// nil if it can't be read. A Lua body gets no `io` library (this package's
// doc comment) — this is the one general "read a file's contents" capability
// bound from Go in its place, for the case plist.read/glob.match don't cover
// (an archetype's own small config file, e.g. forge-engine's
// forge.workspace.toml project list).
func fsRead(L *lua.LState) int {
	path := L.CheckString(1)
	data, err := os.ReadFile(path)
	if err != nil {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(lua.LString(data))
	return 1
}

// --- plist -------------------------------------------------------------
//
// A minimal XML-plist reader for the one thing an archetype body needs
// (reading a value out of Info.plist — a bundle identifier, a version
// string): no third-party dependency, encoding/xml only. Binary plists and
// writing are out of scope; a body that needs either is a follow-up, not a
// silent gap this ticket should paper over with an incomplete answer.

type plistXML struct {
	XMLName xml.Name     `xml:"plist"`
	Dict    plistDictXML `xml:"dict"`
}

type plistDictXML struct {
	Keys   []string `xml:"key"`
	Values []plistValueXML
}

// plistValueXML captures each non-<key> child of <dict> in document order,
// which is required to zip back up against Keys (encoding/xml gives no
// other way to read heterogeneous sibling elements interleaved with <key>).
type plistValueXML struct {
	XMLName xml.Name
	Content string `xml:",chardata"`
}

// UnmarshalXML pairs each <key> with the element that follows it, since
// dict's children alternate <key>k</key><T>v</T> and encoding/xml has no
// built-in "keyed pairs" decoding.
func (d *plistDictXML) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if _, isEnd := tok.(xml.EndElement); isEnd {
				return nil
			}
			continue
		}
		if se.Name.Local == "key" {
			var k string
			if err := dec.DecodeElement(&k, &se); err != nil {
				return err
			}
			d.Keys = append(d.Keys, k)
			continue
		}
		var v plistValueXML
		if err := dec.DecodeElement(&v, &se); err != nil {
			return err
		}
		v.XMLName = se.Name
		d.Values = append(d.Values, v)
	}
}

// plistReadDict reads an XML-format .plist's top-level <dict> into a
// map[string]any, converting <true/>/<false/>/<integer>/<real> to their
// scalar Go types and leaving everything else (<string>, <array>, nested
// <dict> as raw text) as a string — good enough for the bundle-identifier /
// version-string reads an archetype body actually performs.
func plistReadDict(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var p plistXML
	if err := xml.NewDecoder(f).Decode(&p); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for i, k := range p.Dict.Keys {
		if i >= len(p.Dict.Values) {
			break
		}
		v := p.Dict.Values[i]
		switch v.XMLName.Local {
		case "true":
			out[k] = true
		case "false":
			out[k] = false
		case "integer":
			n, _ := strconv.ParseInt(v.Content, 10, 64)
			out[k] = n
		case "real":
			n, _ := strconv.ParseFloat(v.Content, 64)
			out[k] = n
		default:
			out[k] = v.Content
		}
	}
	return out, nil
}

// plistRead implements plist.read(path): reads an XML .plist's top-level
// dict as a Lua table.
func plistRead(L *lua.LState) int {
	path := L.CheckString(1)
	dict, err := plistReadDict(path)
	if err != nil {
		L.RaiseError("plist.read: %v", err)
		return 0
	}
	L.Push(toLuaValue(L, dict))
	return 1
}
