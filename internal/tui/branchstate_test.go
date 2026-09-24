package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

const (
	day  = 24 * time.Hour
	week = 7 * day
)

// TestClassifyProofTier pins the proof tier: every case that fires alone, and the
// precedence between them. These verdicts must stay confCertain — a batch sweep
// reads exactly that flag to decide what it may delete unattended.
func TestClassifyProofTier(t *testing.T) {
	cases := []struct {
		name string
		sig  branchSignals
		want string
	}{
		{"gone", branchSignals{gitSignals: gitSignals{gone: true}}, "gone"},
		{"merged PR", branchSignals{mergedPR: true}, "merged"},
		{"merged locally", branchSignals{gitSignals: gitSignals{mergedLocal: true}}, "merged"},
		{"superseded", branchSignals{superseded: true}, "superseded"},
		{"closed with tip match", branchSignals{closedPR: true, closedTipMatch: true}, "closed"},
		{"empty", branchSignals{gitSignals: gitSignals{empty: true}}, "empty"},
		{"on origin", branchSignals{gitSignals: gitSignals{fullyPushed: true}}, "on origin"},

		// A branch that gained local commits after its PR merged is live work again,
		// not leftovers — the merged and superseded signals are gated on !ahead.
		{"merged then re-committed", branchSignals{mergedPR: true, gitSignals: gitSignals{ahead: true}}, ""},
		{"superseded then re-committed", branchSignals{superseded: true, gitSignals: gitSignals{ahead: true}}, ""},

		// Own PR landing outranks someone else's, so a branch you merged yourself
		// never reads as taken over.
		{"merged outranks superseded", branchSignals{mergedPR: true, superseded: true}, "merged"},
		// A never-committed branch is empty, not merged, even though `git branch
		// --merged` lists it.
		{"empty outranks merged locally", branchSignals{gitSignals: gitSignals{empty: true, mergedLocal: true}}, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := classifyBranch(tc.sig)
			if st.reason != tc.want {
				t.Errorf("reason = %q, want %q", st.reason, tc.want)
			}
			if tc.want == "" {
				return
			}
			if st.conf != confCertain {
				t.Errorf("a proof produced conf %v, want confCertain — a sweep would skip it", st.conf)
			}
			if len(st.because) != 0 {
				t.Errorf("a proof carried evidence words %v; its reason is the explanation", st.because)
			}
		})
	}
}

// TestProtectedIsNeverDead pins that the default branch survives both tiers. The
// proof tier already refused it; the point here is that the evidence tier can't
// reach around and condemn it once the repo goes quiet for a fortnight.
func TestProtectedIsNeverDead(t *testing.T) {
	sig := branchSignals{
		protected:  true,
		gitSignals: gitSignals{mergedLocal: true, fullyPushed: true, idle: 8 * week, behind: 5000},
	}
	if st := classifyBranch(sig); st.reason != "" || st.conf != confLive {
		t.Errorf("protected branch classified %q/%v; main would appear in Cleanup", st.reason, st.conf)
	}
}

// TestOpenPRVetoesEvidence pins the veto. Waiting on a reviewer is the most common
// way a legitimate branch goes quiet for weeks, so an open PR must outrank every
// circumstantial signal — otherwise the dashboard offers to delete the branch
// whose review you are waiting on.
func TestOpenPRVetoesEvidence(t *testing.T) {
	sig := branchSignals{
		openPR:     true,
		closedPR:   true, // an earlier PR was closed, then a new one opened from the same branch
		gitSignals: gitSignals{neverPushed: false, idle: 6 * week, behind: 900},
	}
	if st := classifyBranch(sig); st.reason != "" {
		t.Errorf("a branch with an open PR classified %q — it is under review, not abandoned", st.reason)
	}
}

// TestProofBeatsEvidence pins that the tiers don't compete. A branch can be both
// provably merged and circumstantially abandoned; the proof must win, because
// "merged" names a fact the reader can check and "stale" is a guess.
func TestProofBeatsEvidence(t *testing.T) {
	sig := branchSignals{
		mergedPR:   true,
		closedPR:   true,
		gitSignals: gitSignals{idle: 9 * week, behind: 4000},
	}
	st := classifyBranch(sig)
	if st.reason != "merged" || st.conf != confCertain {
		t.Errorf("got %q/%v, want merged/confCertain", st.reason, st.conf)
	}
}

// TestEvidenceTier walks the circumstantial tier with the real branches this was
// built from — the ones a cleanup of a 36-branch repo had to find by hand because
// the proof tier could not see them at all.
func TestEvidenceTier(t *testing.T) {
	cases := []struct {
		name       string
		sig        branchSignals
		wantStale  bool
		wantReason []string // substrings the evidence line must name
	}{
		{
			// Never pushed, three weeks idle, 747 behind main. Invisible before this
			// tier. behind is measured against the DEFAULT branch, so it genuinely
			// combines with neverPushed — measured against the branch's own origin
			// copy (BranchInfo.Behind) it would always be 0 here, since there is no
			// origin copy to count against.
			name:       "chat-mod-improvements",
			sig:        branchSignals{gitSignals: gitSignals{neverPushed: true, idle: 3 * week, behind: 747}},
			wantStale:  true,
			wantReason: []string{"never pushed", "idle", "747 behind"},
		},
		{
			// Five weeks idle, 1055 behind — the oldest orphan in the repo.
			name:      "1229-cf-gate-sweep",
			sig:       branchSignals{gitSignals: gitSignals{neverPushed: true, idle: 5 * week, behind: 1055}},
			wantStale: true,
		},
		{
			// Its PR was closed unmerged. Still on origin, so no proof fires; the
			// closed PR plus a week of idleness is what carries it.
			name:       "1436-challengeday-lock",
			sig:        branchSignals{closedPR: true, gitSignals: gitSignals{idle: 8 * day, behind: 509}},
			wantStale:  true,
			wantReason: []string{"PR closed"},
		},
		{
			// Two days old, never pushed, 150 behind — weight 3, under the threshold.
			// This is the case the clock is for: it looks exactly like an abandoned
			// branch except that it is this week's work.
			name:      "1384-waitlist-registry-removal (fresh)",
			sig:       branchSignals{gitSignals: gitSignals{neverPushed: true, idle: 2 * day, behind: 150}},
			wantStale: false,
		},
		{
			// Idle for a year but pushed, no PR closed, keeping pace with main. Being
			// old is not evidence of being abandoned.
			name:      "old but pushed and current",
			sig:       branchSignals{gitSignals: gitSignals{idle: 52 * week, behind: 0}},
			wantStale: false,
		},
		{
			// A branch whose activity could not be read has idle 0. An unreadable
			// clock must weigh as no evidence, never as maximally idle.
			name:      "unreadable activity",
			sig:       branchSignals{gitSignals: gitSignals{neverPushed: true, idle: 0, behind: 5000}},
			wantStale: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := classifyBranch(tc.sig)
			gotStale := st.reason == "stale"
			if gotStale != tc.wantStale {
				weight, because := weighEvidence(tc.sig)
				t.Fatalf("stale = %v, want %v (weight %d, evidence %v)", gotStale, tc.wantStale, weight, because)
			}
			if !tc.wantStale {
				if st.conf != confLive {
					t.Errorf("conf = %v, want confLive", st.conf)
				}
				return
			}
			if st.conf != confLikely {
				t.Errorf("conf = %v, want confLikely — a sweep must not take a guess", st.conf)
			}
			if len(st.because) == 0 {
				t.Error("a stale verdict named no evidence; the row cannot show its working")
			}
			line := strings.Join(st.because, " · ")
			for _, want := range tc.wantReason {
				if !strings.Contains(line, want) {
					t.Errorf("evidence %q does not mention %q", line, want)
				}
			}
		})
	}
}

// TestGoneDoesNotDoubleCountAsNeverPushed pins the one place the two tiers could
// contaminate each other. A branch whose upstream was deleted is `gone` — a proof
// — and its gatherer must not also mark it neverPushed, or a proof's own evidence
// would vote a second time in the tier below it.
func TestGoneDoesNotDoubleCountAsNeverPushed(t *testing.T) {
	// This is the shape loadLocalCmd builds for a branch deleted on the remote:
	// HasRemote false, UpstreamGone true -> gone true, neverPushed false.
	sig := branchSignals{gitSignals: gitSignals{gone: true, neverPushed: false}}
	st := classifyBranch(sig)
	if st.reason != "gone" || st.conf != confCertain {
		t.Fatalf("got %q/%v, want gone/confCertain", st.reason, st.conf)
	}
	if len(st.because) != 0 {
		t.Errorf("a gone branch carried circumstantial evidence %v", st.because)
	}
}

// TestReusedBranchNameIsNotMerged pins the gate that protects a branch name reused
// after an earlier PR merged under it. The head sets accumulate and never expire,
// so that name is in MergedHeads permanently, and the new branch's commits have
// never left the machine. The tip the PR merged is not the tip this branch points
// at, so mergedTipMatch is false and the not-ahead gate is all that is left — which
// is why `ahead` has to mean "holds commits origin has never seen" for a branch
// with no origin counterpart too, filled from the unpushed check rather than from
// BranchInfo.Ahead (always 0 without a remote).
func TestReusedBranchNameIsNotMerged(t *testing.T) {
	sig := branchSignals{
		mergedPR:   true, // the OLD branch of this name merged, months ago
		gitSignals: gitSignals{neverPushed: true, ahead: true},
	}
	if st := classifyBranch(sig); st.reason == "merged" {
		t.Error("a recreated branch reported merged; its commits have never left the machine")
	}
}

// TestSquashMergedBranchIsMergedWhileAhead pins the case the not-ahead gate used to
// swallow whole. A squash merge rewrites the commits, so nothing the branch holds is
// reachable from the default branch; deleting the remote copy afterwards leaves the
// local ref as the only place they exist, and the unpushed check therefore reports
// ahead. Before the tip match, `mergedPR && !ahead` vetoed the proof for every one
// of them — in the repo this was found on, seven branches whose PRs had merged were
// invisible to Cleanup permanently, with no evidence-tier weight to catch them
// either. The SHA GitHub merged answers the question the gate was asking without
// also vetoing the ordinary case.
func TestSquashMergedBranchIsMergedWhileAhead(t *testing.T) {
	sig := branchSignals{
		mergedPR:       true,
		mergedTipMatch: true, // the local ref points at exactly what GitHub merged
		gitSignals:     gitSignals{neverPushed: true, ahead: true},
	}
	st := classifyBranch(sig)
	if st.reason != "merged" || st.conf != confCertain {
		t.Fatalf("got %q/%v, want merged/confCertain", st.reason, st.conf)
	}
}

// TestClosedTipMatchBeatsFullyPushed pins the fix this proof exists for: a
// pushed branch whose PR was closed unmerged used to read as "on origin" (a
// pushed-but-live reading landedProof deliberately refuses to treat as a proof)
// because fullyPushed fired first and the closed-PR signal was only ever
// circumstantial. With the tip match wired in, "closed" must fire before
// fullyPushed is ever reached.
func TestClosedTipMatchBeatsFullyPushed(t *testing.T) {
	sig := branchSignals{
		closedPR:       true,
		closedTipMatch: true,
		gitSignals:     gitSignals{fullyPushed: true},
	}
	st := classifyBranch(sig)
	if st.reason != "closed" || st.conf != confCertain {
		t.Fatalf("got %q/%v, want closed/confCertain", st.reason, st.conf)
	}
}

// TestClosedPRWithDifferentTipIsNotProof pins that a closed PR whose head no
// longer matches the local tip stays in the circumstantial tier — the branch
// has moved on since the PR was declined, so deleting it is no longer free.
// TestEvidenceTier's "1436-challengeday-lock" case already exercises this exact
// shape (closedPR without closedTipMatch reaching "stale" via the evidence
// tier); this pins the negative directly: it must never reach confCertain.
func TestClosedPRWithDifferentTipIsNotProof(t *testing.T) {
	sig := branchSignals{
		closedPR:       true,
		closedTipMatch: false,
		gitSignals:     gitSignals{idle: 8 * day, behind: 509},
	}
	if st := classifyBranch(sig); st.conf == confCertain {
		t.Errorf("a closed PR with a mismatched tip produced confCertain (%q); it is not a proof", st.reason)
	}
}

// TestClosedTipMatchEmptyOIDNeverMatches pins the join-level guard in
// Model.branchState: a blank OID on either side (a PR fetched before it carried
// a SHA, or a branch whose tip could not be read) must never compare equal to
// another blank and manufacture a match. Mirrors TestReusedBranchNameIsNotMerged
// for the merged side.
func TestClosedTipMatchEmptyOIDNeverMatches(t *testing.T) {
	m := seededModel()
	m.local.branches = []git.BranchInfo{{Name: "declined", HasRemote: true, LastCommit: "1 hour ago"}}
	m.local.signals = map[string]gitSignals{"declined": {}}
	m.local.tips = map[string]string{} // tip unreadable — blank
	m.remote.ClosedHeads = map[string]string{"declined": ""}
	st := m.branchState("declined")
	if st.signals.closedTipMatch {
		t.Error("two blank OIDs compared equal; closedTipMatch must require a non-empty match")
	}
	if st.reason == "closed" {
		t.Errorf("a blank-OID closed PR produced the closed proof: %q", st.reason)
	}
}

// TestEvidenceThresholdCombinations pins which combinations clear the bar, so a
// later weight change has to face the cases the numbers were chosen for.
func TestEvidenceThresholdCombinations(t *testing.T) {
	cases := []struct {
		name string
		sig  gitSignals
		pr   bool // closedPR
		want bool
	}{
		{"never pushed alone", gitSignals{neverPushed: true}, false, false},
		{"long idle alone", gitSignals{idle: 3 * week}, false, false},
		{"far behind alone", gitSignals{behind: 5000}, false, false},
		{"closed PR alone", gitSignals{}, true, false},
		{"closed PR + short idle", gitSignals{idle: 8 * day}, true, true},
		{"never pushed + long idle", gitSignals{neverPushed: true, idle: 3 * week}, false, true},
		{"never pushed + short idle", gitSignals{neverPushed: true, idle: 8 * day}, false, false},
		{"never pushed + short idle + behind", gitSignals{neverPushed: true, idle: 8 * day, behind: 400}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			weight, _ := weighEvidence(branchSignals{gitSignals: tc.sig, closedPR: tc.pr})
			if got := weight >= evidenceThreshold; got != tc.want {
				t.Errorf("weight %d clears threshold %d = %v, want %v", weight, evidenceThreshold, got, tc.want)
			}
		})
	}
}
