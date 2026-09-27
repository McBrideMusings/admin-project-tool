// Package dockerrun renders and compares `docker run` argv between three
// representations: an admin.toml [docker_run] table (cfg->argv), a `docker
// inspect` JSON blob (inspect->argv), and the resulting flat argv lists
// themselves (drift compare). It is pure, no I/O, no host functions — so it
// is unit-testable without a Lua VM or a live Docker daemon. internal/luahost
// binds it into a `dockerrun` Lua table for the docker archetype's
// run-args/deploy/diff actions to call, so the field table is declared
// exactly once (issue #388's own lesson: before it existed, the cfg->argv
// and inspect->argv directions drifted apart because each was hand-written
// with no shared source).
package dockerrun

// Shape describes how a FieldRow's value renders into argv pieces.
type Shape string

const (
	ShapeRepeated Shape = "repeated"
	ShapeBoolean  Shape = "boolean"
	ShapeKV       Shape = "kv"
	ShapeSingle   Shape = "single"
)

// FieldRow is one row of the field table both render directions read from.
type FieldRow struct {
	Key        string // [docker_run] TOML key
	Flag       string // docker run flag
	Shape      Shape
	Section    string // "host" or "config" — ignored when Reader is set
	InspectKey string

	KVSep            string // default "="
	KVOmitSepIfEmpty bool
	Expand           bool   // cfg value goes through ExpandEnvRefs before rendering
	ImageField       string // "workdir" | "user" — live value stripped when it equals the image default
	DeclaredOnly     bool   // live value stripped entirely unless cfg declares this key
	InspectTransform func(raw any) []string
	Reader           func(hostCfg, config, netSettings map[string]any) any
}

// FieldTable is the single declarative source BuildArgsFromCfg and
// BuildArgsFromInspect both render from.
var FieldTable = []FieldRow{
	{Key: "cap_add", Flag: "--cap-add", Shape: ShapeRepeated, Section: "host", InspectKey: "CapAdd"},
	{Key: "cap_drop", Flag: "--cap-drop", Shape: ShapeRepeated, Section: "host", InspectKey: "CapDrop"},
	{Key: "privileged", Flag: "--privileged", Shape: ShapeBoolean, Section: "host", InspectKey: "Privileged"},
	{Key: "security_opt", Flag: "--security-opt", Shape: ShapeRepeated, Section: "host", InspectKey: "SecurityOpt"},
	{Key: "sysctls", Flag: "--sysctl", Shape: ShapeKV, Section: "host", InspectKey: "Sysctls"},
	{Key: "memory", Flag: "--memory", Shape: ShapeSingle, Section: "host", InspectKey: "Memory"},
	{Key: "user", Flag: "--user", Shape: ShapeSingle, Section: "config", InspectKey: "User", Expand: true, ImageField: "user"},
	{Key: "add_host", Flag: "--add-host", Shape: ShapeRepeated, Section: "host", InspectKey: "ExtraHosts"},
	{Key: "tmpfs", Flag: "--tmpfs", Shape: ShapeKV, Section: "host", InspectKey: "Tmpfs", KVSep: ":", KVOmitSepIfEmpty: true},
	{Key: "group_add", Flag: "--group-add", Shape: ShapeRepeated, Section: "host", InspectKey: "GroupAdd"},
	{Key: "runtime", Flag: "--runtime", Shape: ShapeSingle, Section: "host", InspectKey: "Runtime", DeclaredOnly: true},
	{Key: "ulimits", Flag: "--ulimit", Shape: ShapeRepeated, Section: "host", InspectKey: "Ulimits",
		InspectTransform: func(raw any) []string {
			items, _ := raw.([]any)
			out := make([]string, 0, len(items))
			for _, it := range items {
				u, ok := it.(map[string]any)
				if !ok {
					continue
				}
				name, _ := u["Name"].(string)
				if name == "" {
					continue
				}
				soft := numOrZero(u["Soft"])
				hard := numOrZero(u["Hard"])
				if soft == hard {
					out = append(out, name+"="+formatNum(soft))
				} else {
					out = append(out, name+"="+formatNum(soft)+":"+formatNum(hard))
				}
			}
			return out
		}},
	{Key: "tty", Flag: "-t", Shape: ShapeBoolean, Section: "config", InspectKey: "Tty"},
	{Key: "open_stdin", Flag: "-i", Shape: ShapeBoolean, Section: "config", InspectKey: "OpenStdin"},
	{Key: "hostname", Flag: "--hostname", Shape: ShapeSingle, Section: "config", InspectKey: "Hostname", DeclaredOnly: true},
	{Key: "workdir", Flag: "--workdir", Shape: ShapeSingle, Section: "config", InspectKey: "WorkingDir", ImageField: "workdir"},
	{Key: "mac_address", Flag: "--mac-address", Shape: ShapeSingle, DeclaredOnly: true,
		Reader: func(hostCfg, config, netSettings map[string]any) any {
			netMode, _ := hostCfg["NetworkMode"].(string)
			if netMode == "host" || netMode == "none" || hasPrefix(netMode, "container:") {
				return nil
			}
			if mac, ok := config["MacAddress"].(string); ok && mac != "" {
				return mac
			}
			if mac, ok := netSettings["MacAddress"].(string); ok && mac != "" {
				return mac
			}
			return nil
		}},
}

// BooleanFlags is every boolean-shaped row's flag, derived (not
// hand-maintained) from FieldTable — see ArgvPairAt's doc comment for why
// arity must come from this table rather than "does the next token start
// with a dash".
var BooleanFlags = booleanFlagSet()

func booleanFlagSet() map[string]bool {
	out := map[string]bool{}
	for _, row := range FieldTable {
		if row.Shape == ShapeBoolean {
			out[row.Flag] = true
		}
	}
	return out
}

// liveStripRows indexes FieldTable by flag for rows LiveArgvForDrift
// consults — only rows with ImageField or DeclaredOnly participate.
var liveStripRows = func() map[string]FieldRow {
	out := map[string]FieldRow{}
	for _, row := range FieldTable {
		if row.ImageField != "" || row.DeclaredOnly {
			out[row.Flag] = row
		}
	}
	return out
}()

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
