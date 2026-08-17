"""Go cross-compile dist helper — build a binary for an os/arch matrix, with
an optional git-revision/dirty ldflags stamp and a VERSION file derived from
running the binary's own `-version` flag.

Generic: any Go project configures source_dir/out_dir/binary/targets via a
[go_dist] config table. No project-specific paths live here.
"""

import os
import shlex
import subprocess
import sys

from .core import info, ok, err, run_cmd, resolve_env

DEFAULT_TARGETS = ["darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"]


def _git_short_rev(source_dir):
    try:
        return subprocess.check_output(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=source_dir, text=True, stderr=subprocess.DEVNULL,
        ).strip()
    except Exception:
        return ""


def _git_dirty(source_dir):
    return subprocess.run(
        ["git", "diff", "--quiet", "HEAD"],
        cwd=source_dir, capture_output=True,
    ).returncode != 0


def build_go_dist(cfg):
    """Cross-compile a Go binary for cfg["targets"] (default DEFAULT_TARGETS).

    cfg keys: source_dir (Go module dir, default "."), out_dir (default "dist"),
    binary (required, output filename prefix), targets (["os/arch", ...]),
    stamp (bool, default True — adds -s -w -X main.buildRevision/buildDirty),
    version_flag (e.g. "-version" — if set, `go run` the binary with that flag
    to derive a version string written to out_dir/VERSION), extra_ldflags,
    trimpath (bool, default True), cgo_enabled (default "0").
    """
    source_dir = resolve_env(cfg.get("source_dir", "."))
    out_dir = resolve_env(cfg.get("out_dir", "dist"))
    binary = cfg.get("binary")
    if not binary:
        err("go_dist.binary not set"); sys.exit(1)
    targets = cfg.get("targets") or DEFAULT_TARGETS
    stamp = cfg.get("stamp", True)
    version_flag = cfg.get("version_flag")
    trimpath = cfg.get("trimpath", True)
    cgo_enabled = str(cfg.get("cgo_enabled", "0"))
    extra_ldflags = cfg.get("extra_ldflags", "")

    os.makedirs(out_dir, exist_ok=True)
    abs_out_dir = os.path.abspath(out_dir)

    ldflags_parts = []
    if stamp:
        revision = _git_short_rev(source_dir)
        dirty = "true" if _git_dirty(source_dir) else ""
        ldflags_parts += ["-s", "-w",
                           "-X", "main.buildRevision=" + revision,
                           "-X", "main.buildDirty=" + dirty]
    if extra_ldflags:
        ldflags_parts.append(extra_ldflags)
    ldflags = " ".join(ldflags_parts)
    ldflags_arg = ("-ldflags " + shlex.quote(ldflags)) if ldflags else ""

    if version_flag:
        rc = subprocess.run(
            "go run " + ldflags_arg + " . " + version_flag,
            shell=True, cwd=source_dir, capture_output=True, text=True,
        )
        if rc.returncode != 0:
            err("version probe failed: " + rc.stderr.strip())
            sys.exit(rc.returncode)
        version = rc.stdout.strip()
        with open(os.path.join(abs_out_dir, "VERSION"), "w") as f:
            f.write(version + "\n")
        info("building " + binary + " " + version + " -> " + out_dir)

    trim_arg = "-trimpath " if trimpath else ""
    count = 0
    for target in targets:
        os_name, arch = target.split("/")
        bin_path = os.path.join(abs_out_dir, binary + "-" + os_name + "-" + arch)
        info("  " + os_name + "/" + arch + " -> " + os.path.basename(bin_path))
        build_cmd = (
            "GOOS=" + os_name + " GOARCH=" + arch + " CGO_ENABLED=" + cgo_enabled + " "
            "go build " + trim_arg + ldflags_arg + " -o " + shlex.quote(bin_path) + " ."
        )
        rc = run_cmd("cd " + shlex.quote(source_dir) + " && " + build_cmd)
        if rc != 0:
            err("build failed for " + os_name + "/" + arch)
            return rc
        count += 1

    ok("done — " + str(count) + " binaries" + (" + VERSION" if version_flag else ""))
    return 0
