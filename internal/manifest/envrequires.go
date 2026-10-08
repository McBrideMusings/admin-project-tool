package manifest

import (
	"fmt"
	"os"
	"strings"
)

// parseEnv reads the top-level [env] table. Its one key, `requires`, lists
// environment variables the project cannot run without — the names a .env
// supplies. Each must be a shell identifier, since that is the only shape
// LoadDotenv applies and the only shape ${VAR} can reference.
func parseEnv(data map[string]any, source string) ([]string, error) {
	env, err := table(data, "env", source)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownKeys(env, EnvKeys, "[env]", source); err != nil {
		return nil, err
	}
	names, err := stringSlice(env, "requires", source)
	if err != nil {
		return nil, errf("%s: '[env].requires' must be a list of variable names", source)
	}
	for _, n := range names {
		if !envKeyRe.MatchString(n) {
			return nil, errf("%s: '[env].requires' has %q, which is not a variable name", source, n)
		}
	}
	return names, nil
}

// MissingEnv returns every name in required whose value in the process
// environment is unset or empty, in declared order. Empty counts as missing:
// `ssh ${UNRAID_HOST}` fails the same way whether the .env line is absent or
// reads `UNRAID_HOST=`.
func MissingEnv(required []string) []string {
	var missing []string
	for _, n := range required {
		if os.Getenv(n) == "" {
			missing = append(missing, n)
		}
	}
	return missing
}

// CheckEnvRequires is the one check both `admin check` and dispatch run for
// [env].requires, so the two never disagree about the names or the fix.
func CheckEnvRequires(required []string) error {
	missing := MissingEnv(required)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("[env].requires: unset or empty: %s — set each in this project's .env or export it",
		strings.Join(missing, ", "))
}
