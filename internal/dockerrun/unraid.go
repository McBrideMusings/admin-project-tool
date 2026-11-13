package dockerrun

import (
	"encoding/xml"
	"strings"
)

// UnraidTemplateDir is where Unraid's Docker GUI looks up a container's
// template: the Edit form and "force update" both rebuild the container from
// the file there whose <Name> matches it.
const UnraidTemplateDir = "/boot/config/plugins/dockerMan/templates-user"

// unraidManagedLabel is the label dockerMan adds to every container it
// creates. Its own command builder writes it, so a template never lists it.
const unraidManagedLabel = "net.unraid.docker.managed"

// UnraidTemplate renders the dockerMan template that recreates the container
// `docker run -d --name <container> <argv...> <image>` creates. argv is the
// deploy's own rendered [docker_run] (BuildArgsFromCfg), values already
// filled, so a GUI Apply rebuilds the container the deploy built rather than
// one drawn from a second, hand-kept description of it.
//
// -v, -p, -e and --label become the form's Path, Port, Variable and Label
// fields, and --network its Network; every other token (restart policy,
// --gpus, devices, field-table flags, an anonymous volume) goes into
// ExtraParams, shell-quoted, in its original order; --privileged sets
// <Privileged>. A variable whose name
// looks like a credential (IsSecretEnvName) gets Mask="true".
func UnraidTemplate(container, image string, argv []string) string {
	network, privileged := "bridge", "false"
	var configs, extra []string

	// Every flag BuildArgsFromCfg emits takes one value except FieldTable's
	// boolean rows, so arity comes from BooleanFlags, never from whether the
	// next token starts with a dash.
	for i := 0; i < len(argv); i++ {
		flag := argv[i]
		if BooleanFlags[flag] {
			if flag == "--privileged" {
				privileged = "true"
			} else {
				extra = append(extra, shq(flag))
			}
			continue
		}
		if i+1 >= len(argv) {
			extra = append(extra, shq(flag))
			break
		}
		value := argv[i+1]
		i++
		switch flag {
		case "-v":
			parts := strings.SplitN(value, ":", 3)
			if len(parts) < 2 {
				extra = append(extra, "-v", shq(value))
				continue
			}
			mode := "rw"
			if len(parts) == 3 {
				mode = parts[2]
			}
			configs = append(configs, unraidConfig(parts[1], parts[1], mode, "Path", false, parts[0]))
		case "-p":
			spec, proto := value, "tcp"
			if idx := strings.LastIndex(spec, "/"); idx >= 0 {
				spec, proto = spec[:idx], spec[idx+1:]
			}
			hostSide, target := "", spec
			if idx := strings.LastIndex(spec, ":"); idx >= 0 {
				hostSide, target = spec[:idx], spec[idx+1:]
			}
			if hostSide == "" {
				hostSide = target
			}
			configs = append(configs, unraidConfig("Port "+target, target, proto, "Port", false, hostSide))
		case "-e":
			name, val, _ := strings.Cut(value, "=")
			configs = append(configs, unraidConfig(name, name, "", "Variable", IsSecretEnvName(name), val))
		case "--label":
			key, val, _ := strings.Cut(value, "=")
			if key != unraidManagedLabel {
				configs = append(configs, unraidConfig(key, key, "", "Label", false, val))
			}
		case "--network":
			network = value
		default:
			extra = append(extra, shq(flag), shq(value))
		}
	}

	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n")
	b.WriteString("<!-- Written by `admin deploy` from [docker_run] in admin.toml. Edit admin.toml\n")
	b.WriteString("     and the env file it reads, then deploy: a change made in this form is\n")
	b.WriteString("     overwritten by the next deploy. -->\n")
	b.WriteString("<Container version=\"2\">\n")
	writeElem(&b, "Name", container)
	writeElem(&b, "Repository", image)
	writeElem(&b, "Network", network)
	writeElem(&b, "Privileged", privileged)
	writeElem(&b, "ExtraParams", strings.Join(extra, " "))
	for _, c := range configs {
		b.WriteString(c)
	}
	b.WriteString("</Container>\n")
	return b.String()
}

func unraidConfig(name, target, mode, typ string, mask bool, value string) string {
	maskAttr := "false"
	if mask {
		maskAttr = "true"
	}
	return "  <Config Name=\"" + xmlEscape(name) + "\" Target=\"" + xmlEscape(target) +
		"\" Default=\"\" Mode=\"" + xmlEscape(mode) + "\" Description=\"\" Type=\"" + typ +
		"\" Display=\"always\" Required=\"false\" Mask=\"" + maskAttr + "\">" + xmlEscape(value) + "</Config>\n"
}

func writeElem(b *strings.Builder, name, value string) {
	b.WriteString("  <" + name + ">" + xmlEscape(value) + "</" + name + ">\n")
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
