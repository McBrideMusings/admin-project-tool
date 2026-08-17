"""Install an agent-facing SKILL.md (plus reference/*.md) into a coding agent's
skill directories — ~/.claude/skills/<name> and ~/.codex/skills/<name> by
default — so an agent working in the project can discover its CLI/tooling.

Generic: any project ships a `SKILL.md` (+ optional `reference/*.md`) under a
source directory and wires an `agent-skill` command with the two lines below.
No project-specific paths live here.

    [actions.agent-skill]
    kind = "python"
    run  = '''
    install_agent_skill("agent-skill", "term-server-ctl")
    '''

Refuses to overwrite a skill it did not write: every file it installs carries
an HTML-comment marker line, and a target that exists without that marker is
treated as hand-written and left alone.
"""

import os

from .core import info, ok

MARKER = "<!-- installed by admin: agent_skill.install_agent_skill() -->"

DEFAULT_TARGETS = (
    os.path.expanduser("~/.claude/skills/{name}"),
    os.path.expanduser("~/.codex/skills/{name}"),
)


def _has_marker(path):
    try:
        with open(path, encoding="utf-8", errors="ignore") as f:
            return MARKER in f.read()
    except OSError:
        return False


def _install_reference_file(src, dest, counts):
    if os.path.exists(dest) and not _has_marker(dest):
        info(f"  skip  {dest} (not ours — leaving your version alone)")
        counts["skipped"] += 1
        return
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    with open(src, encoding="utf-8") as f:
        body = f.read()
    with open(dest, "w", encoding="utf-8") as f:
        f.write(MARKER + "\n" + body)
    info(f"  write {dest}")
    counts["installed"] += 1


def _install_skill_md(src, dest, counts):
    # Frontmatter must stay first for the skill to be recognised, so the
    # marker goes after the closing `---` rather than at the top of the file.
    if os.path.exists(dest) and not _has_marker(dest):
        info(f"  skip  {dest} (not ours — leaving your version alone)")
        counts["skipped"] += 1
        return
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    with open(src, encoding="utf-8") as f:
        lines = f.read().splitlines()
    out = []
    in_front = False
    marker_written = False
    for i, line in enumerate(lines):
        if i == 0 and line == "---":
            in_front = True
            out.append(line)
            continue
        if in_front and line == "---":
            out.append(line)
            out.append("")
            out.append(MARKER)
            in_front = False
            marker_written = True
            continue
        out.append(line)
    if not marker_written:
        # No frontmatter found — fall back to a leading marker.
        out = [MARKER, ""] + out
    with open(dest, "w", encoding="utf-8") as f:
        f.write("\n".join(out) + "\n")
    info(f"  write {dest}")
    counts["installed"] += 1


def install_agent_skill(src_dir, name, targets=None):
    """Install the SKILL.md (+ reference/*.md) under `src_dir` into every
    `targets` directory (default: ~/.claude/skills/<name>, ~/.codex/skills/<name>).

    `src_dir` is repo-relative. Returns (installed_count, skipped_count).
    """
    src_dir = os.path.abspath(src_dir)
    skill_md = os.path.join(src_dir, "SKILL.md")
    if not os.path.isfile(skill_md):
        raise SystemExit(f"install_agent_skill: no SKILL.md under {src_dir}")

    dests = list(targets) if targets else [t.format(name=name) for t in DEFAULT_TARGETS]
    counts = {"installed": 0, "skipped": 0}

    ref_dir = os.path.join(src_dir, "reference")
    ref_files = []
    if os.path.isdir(ref_dir):
        ref_files = sorted(
            os.path.join(ref_dir, f) for f in os.listdir(ref_dir) if f.endswith(".md")
        )

    for dest in dests:
        info(dest)
        _install_skill_md(skill_md, os.path.join(dest, "SKILL.md"), counts)
        for f in ref_files:
            _install_reference_file(
                f, os.path.join(dest, "reference", os.path.basename(f)), counts
            )

    ok(f"installed {counts['installed']} file(s), skipped {counts['skipped']}")
    return counts["installed"], counts["skipped"]
