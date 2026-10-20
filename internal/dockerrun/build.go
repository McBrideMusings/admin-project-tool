package dockerrun

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BuildParams is everything BuildImageCommand needs to render the one
// `docker build` command line the docker archetype's build and deploy actions
// both run.
type BuildParams struct {
	// BuildCfg is the [docker_build] table (dockerfile, args; on_host and
	// context are read by the caller and passed below).
	BuildCfg map[string]any
	Target   string // [docker_run].target; empty means no --target
	Image    string
	// OnHost builds inside Context on Host (an ssh hop unless IsLocal);
	// otherwise the build runs here for linux/amd64.
	OnHost  bool
	Context string
	Host    string
	IsLocal bool
	// ProjectRoot is the directory holding admin.toml; ${GIT_REVISION} is the
	// HEAD of the repository there.
	ProjectRoot string
	// Now is the clock ${BUILD_TIME} reads; zero means time.Now.
	Now time.Time
}

// IsBuildComputed reports whether name is a value BuildArgValues computes
// itself (so `admin check` does not flag it as an unset variable).
func IsBuildComputed(name string) bool {
	return name == "GIT_REVISION" || name == "BUILD_TIME"
}

func quoteAlways(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// gitRevision returns `git rev-parse HEAD` of dir, or "" when git fails.
func gitRevision(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// BuildArgValues expands [docker_build].args into NAME=value pairs sorted by
// name. ${VAR} / ${VAR:-default} resolve against the process environment plus
// two values computed here and visible to this expansion only: ${GIT_REVISION}
// (HEAD of ProjectRoot; unresolved when git fails) and ${BUILD_TIME} (UTC,
// RFC 3339). An explicit environment variable of the same name wins. A
// reference that resolves to nothing is an error naming the arg.
func BuildArgValues(p BuildParams) ([]string, error) {
	raw := getMap(p.BuildCfg, "args")
	if len(raw) == 0 {
		return nil, nil
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	if _, ok := env["GIT_REVISION"]; !ok {
		if rev := gitRevision(p.ProjectRoot); rev != "" {
			env["GIT_REVISION"] = rev
		}
	}
	if _, ok := env["BUILD_TIME"]; !ok {
		now := p.Now
		if now.IsZero() {
			now = time.Now()
		}
		env["BUILD_TIME"] = now.UTC().Format("2006-01-02T15:04:05Z")
	}
	var out []string
	for _, name := range sortedKeys(raw) {
		value, ok := ExpandEnvRefs(toStr(raw[name]), env)
		if !ok {
			return nil, fmt.Errorf("[docker_build].args.%s: unresolved reference in %q", name, toStr(raw[name]))
		}
		out = append(out, name+"="+value)
	}
	return out, nil
}

// BuildImageCommand renders the shell command that builds p.Image: the local
// form (`docker build --platform linux/amd64 ...`), or for OnHost the
// `cd <context> && docker build ...` form wrapped in ssh unless IsLocal.
// It is the only place the docker archetype assembles a build command, so a
// flag added here reaches both `build` and `deploy`, local and on_host.
func BuildImageCommand(p BuildParams) (string, error) {
	buildArgs, err := BuildArgValues(p)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if p.OnHost {
		b.WriteString("cd " + quoteAlways(p.Context) + " && docker build ")
	} else {
		b.WriteString("docker build --platform linux/amd64 ")
	}
	if df := getString(p.BuildCfg, "dockerfile"); df != "" {
		b.WriteString("-f " + quoteAlways(df) + " ")
	}
	if p.Target != "" {
		b.WriteString("--target " + quoteAlways(p.Target) + " ")
	}
	for _, a := range buildArgs {
		b.WriteString("--build-arg " + quoteAlways(a) + " ")
	}
	b.WriteString("-t " + quoteAlways(p.Image) + " .")
	cmd := b.String()
	if p.OnHost && !p.IsLocal {
		return "ssh " + quoteAlways(p.Host) + " " + quoteAlways(cmd), nil
	}
	return cmd, nil
}
