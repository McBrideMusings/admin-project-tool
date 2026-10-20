package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/dockerrun"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// unresolvedEnvRef is one config-table ${VAR} with no default that no
// environment value satisfies: Origin is its dotted key, State says whether
// the variable is unset or set to the empty string (ADR-0013 treats the two
// alike).
type unresolvedEnvRef struct {
	Var, Origin, State string
}

// checkConfigEnvRefs reports every config-table ${VAR} that would abort the
// next run of the command that reads it. Command and action `run` strings are
// deliberately not checked: they go through manifest.ResolveComputed, which
// hands an unresolved ${VAR} to the shell on purpose, so a shell-local there
// is legal. It sees the same environment a real run does: the project's .env
// is already loaded by resolveProjectManifest.
func checkConfigEnvRefs(m *manifest.Manifest) error {
	bad := unresolvedConfigEnvRefs(m)
	if len(bad) == 0 {
		return nil
	}
	lines := make([]string, 0, len(bad)+1)
	for _, b := range bad {
		lines = append(lines, fmt.Sprintf("%s = ${%s} — %s is %s and the reference has no default", b.Origin, b.Var, b.Var, b.State))
	}
	lines = append(lines, "give the reference a default (${VAR:-value}), export the variable, or remove the key")
	return validate.Errorf("%s", strings.Join(lines, "\n"))
}

func unresolvedConfigEnvRefs(m *manifest.Manifest) []unresolvedEnvRef {
	var bad []unresolvedEnvRef
	for _, r := range configEnvRefs(m) {
		if r.HasDefault {
			continue
		}
		if _, computed := manifest.ComputedVars[r.Name]; computed {
			continue
		}
		val, set := os.LookupEnv(r.Name)
		if val != "" {
			continue
		}
		state := "not set"
		if set {
			state = "set but empty"
		}
		for _, origin := range r.UsedBy {
			if dockerrun.IsBuildComputed(r.Name) && strings.HasPrefix(origin, "docker_build.args.") {
				continue
			}
			bad = append(bad, unresolvedEnvRef{Var: r.Name, Origin: origin, State: state})
		}
	}
	sort.Slice(bad, func(i, j int) bool {
		if bad[i].Var != bad[j].Var {
			return bad[i].Var < bad[j].Var
		}
		return bad[i].Origin < bad[j].Origin
	})
	return bad
}
