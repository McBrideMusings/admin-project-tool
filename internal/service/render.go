package service

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// RenderPlist returns the LaunchAgent plist bytes for c. Pure — no filesystem
// writes, which is what lets Install compare against what is already on disk
// and skip re-registration when nothing changed (see the package doc's
// Background Task Management note).
//
// Keys are emitted sorted, so a plist written by an earlier run and one
// written now compare equal byte-for-byte and an upgrade does not look like
// a change.
func RenderPlist(c Config) []byte {
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	if c.InheritPath {
		if _, ok := env["PATH"]; !ok {
			env["PATH"] = os.Getenv("PATH")
		}
	}
	if c.InheritHome {
		if _, ok := env["HOME"]; !ok {
			home, _ := os.UserHomeDir()
			env["HOME"] = home
		}
	}

	entries := map[string]string{
		"Label":     xmlString(c.Label),
		"RunAtLoad": xmlBool(c.RunAtLoad),
		"KeepAlive": xmlBool(c.KeepAlive),
	}

	var argv strings.Builder
	argv.WriteString("<array>\n")
	for _, a := range append([]string{c.Program}, c.Args...) {
		argv.WriteString("\t\t" + xmlString(a) + "\n")
	}
	argv.WriteString("\t</array>")
	entries["ProgramArguments"] = argv.String()

	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("<dict>\n")
		for _, k := range keys {
			b.WriteString("\t\t<key>" + escape(k) + "</key>\n")
			b.WriteString("\t\t" + xmlString(env[k]) + "\n")
		}
		b.WriteString("\t</dict>")
		entries["EnvironmentVariables"] = b.String()
	}
	if c.Throttle != nil {
		entries["ThrottleInterval"] = fmt.Sprintf("<integer>%d</integer>", *c.Throttle)
	}
	if c.StartInterval != nil {
		entries["StartInterval"] = fmt.Sprintf("<integer>%d</integer>", *c.StartInterval)
	}
	if c.Nice != nil {
		entries["Nice"] = fmt.Sprintf("<integer>%d</integer>", *c.Nice)
	}
	if c.WorkingDir != "" {
		entries["WorkingDirectory"] = xmlString(c.WorkingDir)
	}
	if c.Stdout != "" {
		entries["StandardOutPath"] = xmlString(c.Stdout)
	}
	if c.Stderr != "" {
		entries["StandardErrorPath"] = xmlString(c.Stderr)
	}
	if c.ProcessType != "" {
		entries["ProcessType"] = xmlString(c.ProcessType)
	}

	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	for _, k := range keys {
		b.WriteString("\t<key>" + escape(k) + "</key>\n")
		b.WriteString("\t" + entries[k] + "\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String())
}

// RenderSystemdUnit returns the systemd --user unit for c.
func RenderSystemdUnit(c Config) []byte {
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	if c.InheritPath {
		if _, ok := env["PATH"]; !ok {
			env["PATH"] = os.Getenv("PATH")
		}
	}

	parts := make([]string, 0, len(c.Args)+1)
	for _, p := range append([]string{c.Program}, c.Args...) {
		parts = append(parts, shQuote(p))
	}

	lines := []string{
		"[Unit]", "Description=" + c.Label, "After=network-online.target", "",
		"[Service]", "ExecStart=" + strings.Join(parts, " "),
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, "Environment="+k+"="+env[k])
	}
	if c.WorkingDir != "" {
		lines = append(lines, "WorkingDirectory="+c.WorkingDir)
	}
	if c.Stdout != "" {
		lines = append(lines, "StandardOutput=append:"+c.Stdout)
	}
	if c.Stderr != "" {
		lines = append(lines, "StandardError=append:"+c.Stderr)
	}
	if c.KeepAlive {
		restartSec := 5
		if c.Throttle != nil {
			restartSec = *c.Throttle
		}
		lines = append(lines, "Restart=always", fmt.Sprintf("RestartSec=%d", restartSec))
	}
	lines = append(lines, "", "[Install]", "WantedBy=default.target")
	return []byte(strings.Join(lines, "\n") + "\n")
}

func xmlString(s string) string { return "<string>" + escape(s) + "</string>" }

func xmlBool(b bool) string {
	if b {
		return "<true/>"
	}
	return "<false/>"
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, "\t\n '\"$&*()[]{}|;<>?!~`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
