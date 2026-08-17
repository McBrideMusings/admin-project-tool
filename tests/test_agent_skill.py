"""Tests for admin_lib.agent_skill.install_agent_skill — installs a SKILL.md
(+ reference/*.md) into agent skill directories, refusing to overwrite a skill
it did not write."""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import agent_skill  # noqa: E402


def _src(tmp_path):
    src = tmp_path / "agent-skill"
    (src / "reference").mkdir(parents=True)
    (src / "SKILL.md").write_text("---\nname: x\n---\n\nBody.\n")
    (src / "reference" / "notes.md").write_text("notes")
    return src


def test_installs_skill_md_with_marker_after_frontmatter(tmp_path):
    src = _src(tmp_path)
    dest = tmp_path / "target"
    installed, skipped = agent_skill.install_agent_skill(str(src), "x", targets=[str(dest)])
    assert installed == 2 and skipped == 0
    text = (dest / "SKILL.md").read_text()
    assert text.startswith("---\nname: x\n---\n\n")
    assert agent_skill.MARKER in text
    assert (dest / "reference" / "notes.md").read_text().endswith("notes")


def test_refuses_to_overwrite_a_hand_written_skill(tmp_path):
    src = _src(tmp_path)
    dest = tmp_path / "target"
    dest.mkdir()
    (dest / "SKILL.md").write_text("---\nname: mine\n---\nhand-written\n")
    installed, skipped = agent_skill.install_agent_skill(str(src), "x", targets=[str(dest)])
    assert skipped >= 1
    assert (dest / "SKILL.md").read_text() == "---\nname: mine\n---\nhand-written\n"


def test_reinstalling_over_its_own_marker_is_allowed(tmp_path):
    src = _src(tmp_path)
    dest = tmp_path / "target"
    agent_skill.install_agent_skill(str(src), "x", targets=[str(dest)])
    installed, skipped = agent_skill.install_agent_skill(str(src), "x", targets=[str(dest)])
    assert skipped == 0
    assert installed == 2
