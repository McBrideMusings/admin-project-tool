package worktree

import "github.com/McBrideMusings/admin-project-tool/internal/manifest"

// ManifestContentRoot finds the directory to read admin.toml from when
// running against a resolved -w target: the target's own admin.toml wins if
// present (manifest.FindRoot walking up from target); otherwise, since
// admin.toml is never committed and a fresh worktree may simply lack one, it
// falls back to the primary checkout's.
//
// The directory this returns is where the manifest's *content* comes from,
// not where it executes: a manifest borrowed from the primary checkout still
// has to run every step, write its logs, and resolve ${VAR} in the target
// worktree, never in the primary checkout. The caller that resolved target
// (cmd/manifest.go's resolveProjectManifestFromDisk) is responsible for
// overriding the loaded Manifest's Root to target afterward — this function
// only answers "whose admin.toml".
//
// ok is false when neither the target nor the primary checkout has one.
func ManifestContentRoot(target, primary string) (dir string, ok bool) {
	if root, found := manifest.FindRoot(target); found {
		return root, true
	}
	if primary == "" {
		return "", false
	}
	return manifest.FindRoot(primary)
}
