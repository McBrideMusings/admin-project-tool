#!/bin/sh
#
# assert-mac-signature.sh — shared by every `apple` archetype project (via
# admin-project-tool). Judges the signature on a built macOS app and says
# what it will cost. Wire it as a scheme build post-action:
#
#     script: '"$HOME/.admin/admin_lib/resources/assert-mac-signature.sh"'
#
# A project usually commits `CODE_SIGN_IDENTITY: "-"` deliberately: a public
# artifact should not carry anyone's personal certificate, and a fresh clone
# has to build on a machine with no cert at all. So an ad-hoc signature is a
# *sanctioned* outcome here, and this script does not simply reject it. What
# it does is refuse to let the two real consequences stay invisible.
#
# ── 1. Ad-hoc costs you every permission grant, every rebuild (warning) ──────
#
# macOS TCC binds a grant to the program's code-signature identity. Ad-hoc has
# none, so TCC falls back to the cdhash — which changes on every single build.
# Accessibility, Automation, Documents, Screen Recording: each rebuild is a new
# app as far as macOS is concerned, and asks again. There is no error message;
# it reads as macOS being flaky.
#
# The fix is local and puts no certificate in the repo: `[apple] sign_identity =
# "Apple Development"` in the gitignored `admin.toml`. The tool resolves a
# keychain hash by substring match and hands it to xcodebuild for the builds it
# drives. A machine without a cert is unaffected and still builds.
#
# ── 2. Ad-hoc plus embedded code will not launch at all (error) ─────────────
#
# `ENABLE_HARDENED_RUNTIME: YES` turns on library validation: the process may
# only map code signed by Apple or by the app's own team. Ad-hoc has no team, so
# any embedded framework, dylib, or plug-in is refused and the process is killed
# before `main` runs:
#
#     Library not loaded: @loader_path/../Frameworks/<name>.framework/…
#     mapping process and mapped file (non-platform) have different Team IDs
#
# Nothing about the build says so. It prints BUILD SUCCEEDED and the app dies
# on double-click — a real failure of exactly this shape, including the part
# where a check that only looked for the `runtime` flag waved it through, is
# McBrideMusings/iptv-mac#54.
#
# A project that embeds no code (a framework linked but not embedded, no
# plug-ins) never hits case 2 — but that's a property of its current
# dependency list, not something to trust blindly, so this looks for the
# actual condition — a Mach-O in the bundle that is not the main executable —
# rather than assuming today's dependency shape stays true.
#
# ── Why a post-action ───────────────────────────────────────────────────────
#
# Xcode signs the product *after* every build phase of the target finishes, so a
# run-script phase sees an unsigned bundle and `codesign -d` reports "code object
# is not signed at all" on a perfectly good build. A scheme build post-action
# runs after the whole build action, and a non-zero exit from one still fails
# the build.
#
# By hand, against a build that already happened:
#
#     assert-mac-signature.sh /Applications/YourApp.app

set -u

# The scheme post-action gets this from `settingsTarget:`; the argument is for
# running it by hand.
APP="${1:-${CODESIGNING_FOLDER_PATH:-}}"

# Each line carries its own prefix because Xcode forwards a post-action's plain
# output nowhere — `xcodebuild` prints only the lines its parser reads as
# issues, so an untagged line is one the developer never sees.
say() {
    printf '%s\n' "$2" | while IFS= read -r line; do
        echo "$1: $line" >&2
    done
}

fail() {
    say error "$1"
    exit 1
}

if [ -z "$APP" ]; then
    fail "signature check: no app to inspect — CODESIGNING_FOLDER_PATH is empty and no path was given."
fi

if [ ! -d "$APP" ]; then
    fail "signature check: $APP does not exist."
fi

# Signing deliberately off — say so rather than going quiet, since a check that
# went silent here would look exactly like a check that ran and approved.
if [ "${CODE_SIGNING_ALLOWED:-YES}" = "NO" ]; then
    echo "note: signature check skipped — CODE_SIGNING_ALLOWED=NO, so this build is unsigned by request."
    exit 0
fi

DESCRIPTION=$(codesign --display --verbose=2 "$APP" 2>&1) || \
    fail "signature check: codesign could not read $APP:
$DESCRIPTION"

FLAGS=$(printf '%s\n' "$DESCRIPTION" | sed -n 's/.*[ ]flags=\([^ ]*\).*/\1/p')
TEAM=$(printf '%s\n' "$DESCRIPTION" | sed -n 's/^TeamIdentifier=//p' | head -1)

# `codesign` prints `TeamIdentifier=not set` rather than an empty value, so an
# emptiness test alone reads that as a team named "not set".
if [ -z "$TEAM" ] || [ "$TEAM" = "not set" ]; then
    TEAM=""
fi

case "$FLAGS" in
    *adhoc*) ADHOC=yes ;;
    *)       ADHOC=no ;;
esac

if [ "$ADHOC" = "no" ] && [ -n "$TEAM" ]; then
    echo "note: signature ok on $APP (team $TEAM, CodeDirectory flags=$FLAGS)."
    exit 0
fi

# Ad-hoc from here down. Whether that is fatal depends on what is inside the
# bundle: every Mach-O other than the main executable is something dyld will be
# asked to map into a hardened process, and will refuse to.
MAIN_EXECUTABLE=""
if [ -f "$APP/Contents/Info.plist" ]; then
    NAME=$(/usr/bin/plutil -extract CFBundleExecutable raw "$APP/Contents/Info.plist" 2>/dev/null)
    [ -n "$NAME" ] && MAIN_EXECUTABLE="$APP/Contents/MacOS/$NAME"
fi

# One `find | file | grep` pipeline rather than a loop with a `case` in it,
# because /bin/sh here is bash 3.2 and cannot parse a `case` nested inside a
# command substitution — it dies with "syntax error near unexpected token `;;'"
# on code that is valid POSIX. Verified against this machine's
# `bash --version`: 3.2.57(1)-release.
#
# `file` prints "<path>: <description>", so the sed strips from the description
# back, and the main executable — which is signed as part of the app and is not
# a separate thing to map — is dropped by an exact, non-regex match.
#
# The `(for architecture` filter is load-bearing. For a universal binary `file`
# prints one line PER SLICE, and the extra ones read
#
#     <path> (for architecture arm64):	Mach-O 64-bit executable arm64
#
# so stripping the description leaves "<path> (for architecture arm64)", which
# matches no real path and slips past the main-executable exclusion. Without
# this line every fat binary in the bundle — including the app's own, and every
# framework anyone ships universal — is reported as embedded code, and the
# guard fails every ad-hoc build instead of warning about it.
EMBEDDED=$(
    find "$APP" -type f -perm +111 -exec file {} + 2>/dev/null \
        | grep 'Mach-O' \
        | grep -v ' (for architecture ' \
        | sed 's/:[[:space:]]*Mach-O.*$//' \
        | grep -vxF "${MAIN_EXECUTABLE:-/nonexistent}"
)

HOW_TO_FIX='  Set `[apple] sign_identity = "Apple Development"` in admin.toml. The tool
  resolves a keychain hash by substring match and passes it to xcodebuild, so
  no certificate, hash or team id lands in a tracked file — and a machine
  without a cert still builds, ad-hoc, exactly as it does now.'

case "$FLAGS" in
    *runtime*)
        if [ -n "$EMBEDDED" ]; then
            fail "the built app is signed ad-hoc AND embeds code, so it will not launch.
  app:                 $APP
  CodeDirectory flags: $FLAGS
  TeamIdentifier:      not set
  embedded Mach-O:
$(printf '%s\n' "$EMBEDDED" | sed 's/^/    /')
  Hardened runtime enforces library validation: the process may only map code
  signed by Apple or by its own team. Ad-hoc has no team, so dyld kills it at
  launch, before main runs:
    Library not loaded: @loader_path/../Frameworks/<name>.framework/…
    mapping process and mapped file (non-platform) have different Team IDs
$HOW_TO_FIX"
        fi
        ;;
esac

say warning "the built app is signed ad-hoc, so macOS will forget its permissions on every rebuild.
  app:                 $APP
  CodeDirectory flags: ${FLAGS:-none}
  TeamIdentifier:      not set
  A grant is bound to the code-signature identity. Ad-hoc has none, so TCC falls
  back to the cdhash, which changes every build — Accessibility, Automation,
  Documents and the rest are asked for again each time. This is the same
  reasoning admin.toml already applies to term-server (#216).
$HOW_TO_FIX"
exit 0
