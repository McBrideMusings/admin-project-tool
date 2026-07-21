"""Input classification, slug derivation, and branch resolution (P0)."""
from __future__ import annotations

import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import classify, ghwt, resolve, slug


# --- classify ---------------------------------------------------------------

def test_classify_text():
    c = classify.classify("my-feature")
    assert c.kind == classify.TEXT and c.text == "my-feature"


def test_classify_text_with_slash_is_still_text():
    c = classify.classify("feat/existing")
    assert c.kind == classify.TEXT and c.text == "feat/existing"


@pytest.mark.parametrize("inp", ["42", "#42"])
def test_classify_number(inp):
    c = classify.classify(inp)
    assert c.kind == classify.NUMBER and c.number == 42


def test_classify_pr_url():
    c = classify.classify("https://github.com/owner/repo/pull/123")
    assert c.kind == classify.PR and c.number == 123 and c.nwo == "owner/repo"


def test_classify_issue_url():
    c = classify.classify("https://github.com/owner/repo/issues/45")
    assert c.kind == classify.ISSUE and c.number == 45 and c.nwo == "owner/repo"


def test_classify_project_board_url():
    url = "https://github.com/orgs/Org/projects/1/views/2?issue=Org%7Crepo%7C77"
    c = classify.classify(url)
    assert c.kind == classify.ISSUE and c.number == 77 and c.nwo == "Org/repo"


def test_classify_project_board_url_malformed_falls_back():
    url = "https://github.com/orgs/Org/projects/1/views/2"
    c = classify.classify(url)
    # No parseable issue param — not a branch, but must not crash; stays TEXT.
    assert c.kind == classify.TEXT


# --- slug -------------------------------------------------------------------

def test_slug_basic():
    assert slug.slug_from_issue(42, "Fix the login bug") == "42-fix-the-login-bug"


def test_slug_caps_five_words():
    assert slug.slug_from_issue(7, "one two three four five six") == "7-one-two-three-four-five"


def test_slug_strips_punctuation():
    assert slug.slug_from_issue(9, "Add OAuth 2.0 support!") == "9-add-oauth-20-support"


def test_slug_empty_title_fallback():
    assert slug.slug_from_issue(5, "!!!") == "issue-5"


# --- resolve (gh monkeypatched) ---------------------------------------------

def test_resolve_text_passthrough():
    r = resolve.resolve("my-feature")
    assert r.branch == "my-feature" and r.source == "text" and not r.is_pr


def test_resolve_text_keeps_slash():
    assert resolve.resolve("feat/foo").branch == "feat/foo"


def test_resolve_empty_text_errors():
    with pytest.raises(resolve.ResolveError):
        resolve.resolve("   ")


def test_resolve_pr_url_uses_head_ref(monkeypatch):
    monkeypatch.setattr(ghwt, "pr_head_ref", lambda n: "contributor-fix-oauth")
    r = resolve.resolve("https://github.com/owner/repo/pull/123")
    assert r.branch == "contributor-fix-oauth" and r.is_pr and r.source == "pr"


def test_resolve_issue_url_slugs_title(monkeypatch):
    monkeypatch.setattr(ghwt, "issue_title", lambda n: "Fix the thing")
    r = resolve.resolve("https://github.com/owner/repo/issues/45")
    assert r.branch == "45-fix-the-thing" and r.source == "issue"


def test_resolve_number_prefers_pr(monkeypatch):
    monkeypatch.setattr(ghwt, "available", lambda: True)
    monkeypatch.setattr(ghwt, "pr_head_ref", lambda n: "the-pr-branch")
    r = resolve.resolve("42")
    assert r.branch == "the-pr-branch" and r.is_pr


def test_resolve_number_falls_back_to_issue(monkeypatch):
    monkeypatch.setattr(ghwt, "available", lambda: True)

    def boom(n):
        raise ghwt.GhError("no such PR")

    monkeypatch.setattr(ghwt, "pr_head_ref", boom)
    monkeypatch.setattr(ghwt, "issue_title", lambda n: "Some issue")
    r = resolve.resolve("42")
    assert r.branch == "42-some-issue" and not r.is_pr


def test_resolve_number_without_gh_errors(monkeypatch):
    monkeypatch.setattr(ghwt, "available", lambda: False)
    with pytest.raises(resolve.ResolveError):
        resolve.resolve("42")


def _boom_error(*_a):
    raise ghwt.GhError("not found")


def test_resolve_pr_url_gh_error_is_clean(monkeypatch):
    monkeypatch.setattr(ghwt, "pr_head_ref", _boom_error)
    with pytest.raises(resolve.ResolveError):
        resolve.resolve("https://github.com/o/r/pull/999999")


def test_resolve_issue_url_gh_error_is_clean(monkeypatch):
    monkeypatch.setattr(ghwt, "issue_title", _boom_error)
    with pytest.raises(resolve.ResolveError):
        resolve.resolve("https://github.com/o/r/issues/999999")


def test_resolve_number_neither_pr_nor_issue_is_clean(monkeypatch):
    monkeypatch.setattr(ghwt, "available", lambda: True)
    monkeypatch.setattr(ghwt, "pr_head_ref", _boom_error)
    monkeypatch.setattr(ghwt, "issue_title", _boom_error)
    with pytest.raises(resolve.ResolveError):
        resolve.resolve("999999")
