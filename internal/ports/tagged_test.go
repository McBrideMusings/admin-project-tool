package ports

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPlanSweepNeverTargetsUnsafeGroupsOrPIDs guards the sweep's blast
// radius: a tagged process in pgid 0, pgid 1, admin's own group or a
// protected group must be signalled alone (or not at all), never as -pgid,
// or the sweep would kill the shell or admin that owns the run. A group that
// also holds a live process outside the run is signalled pid by pid, so that
// process survives.
func TestPlanSweepNeverTargetsUnsafeGroupsOrPIDs(t *testing.T) {
	matched := []Tagged{
		{PID: 500, PGID: 500},   // own group: safe to signal as a group
		{PID: 501, PGID: 500},   // same group: deduped
		{PID: 502, PGID: 900},   // protected group (admin's own): pid only
		{PID: 503, PGID: 0},     // unresolved pgid: pid only
		{PID: 504, PGID: 1},     // pgid 1: pid only
		{PID: 1, PGID: 1},       // pid 1: dropped
		{PID: 0, PGID: 0},       // pid 0: dropped
		{PID: 700, PGID: 700},   // protected pid: dropped
		{PID: -1, PGID: -1},     // negative: dropped
		{PID: 505, PGID: 505},   // second safe group
		{PID: 506, PGID: 505},   // joins it
		{PID: 507, PGID: 507},   // third
		{PID: 508, PGID: 900},   // protected group: pid only
		{PID: 509, PGID: 18446}, // ordinary group
		{PID: 510, PGID: 600},   // group shared with an untagged process: pid only
		{PID: 511, PGID: 610},   // group shared with a hidden-env system binary: group
	}
	var all []ProcInfo
	for _, m := range matched {
		all = append(all, ProcInfo{PID: m.PID, PGID: m.PGID, EnvReadable: true, RunID: "r"})
	}
	all = append(all,
		ProcInfo{PID: 512, PGID: 600, EnvReadable: true},  // untagged sleeper, readable env
		ProcInfo{PID: 513, PGID: 610, EnvReadable: false}, // /bin/sh: env hidden
	)
	plan := PlanSweep(matched, all, map[int]bool{700: true}, map[int]bool{900: true})

	sort.Ints(plan.Groups)
	sort.Ints(plan.PIDs)
	for _, g := range plan.Groups {
		if g <= 1 || g == 900 || g == 600 {
			t.Errorf("plan signals unsafe group %d", g)
		}
	}
	for _, p := range plan.PIDs {
		if p <= 1 || p == 700 {
			t.Errorf("plan signals unsafe pid %d", p)
		}
	}
	wantPIDs := []int{502, 503, 504, 508, 510}
	if len(plan.PIDs) != len(wantPIDs) {
		t.Fatalf("PIDs = %v, want %v", plan.PIDs, wantPIDs)
	}
	for i, p := range wantPIDs {
		if plan.PIDs[i] != p {
			t.Errorf("PIDs = %v, want %v", plan.PIDs, wantPIDs)
		}
	}
	wantGroups := []int{500, 505, 507, 610, 18446}
	if len(plan.Groups) != len(wantGroups) {
		t.Fatalf("Groups = %v, want %v", plan.Groups, wantGroups)
	}
	for i, g := range wantGroups {
		if plan.Groups[i] != g {
			t.Errorf("Groups = %v, want %v", plan.Groups, wantGroups)
		}
	}
}

// TestHelperProcess is the stand-in dev server: the test binary re-executed
// so that its environment is readable by KERN_PROCARGS2 (a system binary
// such as /bin/sleep hides its environment on macOS).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("PORTS_TEST_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(60 * time.Second)
}

// testRunPrefix makes every run id these tests plant unique to this test
// process, so the sweeps below can be confined (SweepOptions.Scope) to
// processes they started and never touch a real dev server.
var testRunPrefix = fmt.Sprintf("r-test-%d-%d-", os.Getpid(), time.Now().UnixNano())

func plantedOnly(tp Tagged) bool { return strings.HasPrefix(tp.RunID, testRunPrefix) }

func startHelper(t *testing.T, runID, checkout string) *exec.Cmd {
	t.Helper()
	return startHelperIn(t, runID, checkout, 0)
}

// startHelperIn starts a helper; joinPGID > 0 puts it into that existing
// process group, and -1 gives it a group of its own in the test's session;
// 0 starts it in a new session.
func startHelperIn(t *testing.T, runID, checkout string, joinPGID int) *exec.Cmd {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	c.Env = append(os.Environ(), "PORTS_TEST_HELPER=1")
	if runID != "" {
		c.Env = append(c.Env, EnvRunID+"="+runID, EnvCheckout+"="+checkout)
	}
	// Setsid is what Node's `detached: true` does: the child leaves the
	// process group, so a group kill cannot reach it.
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if joinPGID > 0 {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: joinPGID}
	} else if joinPGID < 0 {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // leads its own group, same session
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	go c.Wait() // reap on exit so a signal-0 probe sees the death
	t.Cleanup(func() { _ = c.Process.Kill() })
	return c
}

// TestSweepTaggedKillsDetachedChildOfARunAndSparesOthers guards the reason
// the tag exists: a child that called setsid (outside every process group
// admin owns) is still found by its ADMIN_RUN_ID and killed, while a process
// of a different run, and an untagged one, survive.
func TestSweepTaggedKillsDetachedChildOfARunAndSparesOthers(t *testing.T) {
	mine := startHelper(t, testRunPrefix+"mine", "/checkout/a")
	other := startHelper(t, testRunPrefix+"other", "/checkout/b")
	plain := startHelper(t, "", "")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		for _, p := range TaggedProcesses() {
			if p.PID == mine.Process.Pid {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	SweepTagged("test", func(tp Tagged) bool { return tp.RunID == testRunPrefix+"mine" },
		SweepOptions{Timeout: 2 * time.Second, Scope: plantedOnly})

	if pidAlive(mine.Process.Pid) {
		t.Errorf("tagged detached helper %d survived the sweep", mine.Process.Pid)
	}
	if !pidAlive(other.Process.Pid) {
		t.Errorf("helper of another run %d was killed", other.Process.Pid)
	}
	if !pidAlive(plain.Process.Pid) {
		t.Errorf("untagged helper %d was killed", plain.Process.Pid)
	}
}

// TestNukeTaggedHonorsCheckoutAndAnyCheckout guards the scope of nuke-dev's
// tag pass: by default only this checkout's runs die; --any-checkout reaches
// every run.
func TestNukeTaggedHonorsCheckoutAndAnyCheckout(t *testing.T) {
	root := t.TempDir()
	mineHere := startHelper(t, testRunPrefix+"nuke-here", root)
	elsewhere := startHelper(t, testRunPrefix+"nuke-else", "/some/other/checkout")

	waitTagged := func(pid int) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, p := range TaggedProcesses() {
				if p.PID == pid {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("pid %d never showed up as tagged", pid)
	}
	waitTagged(mineHere.Process.Pid)
	waitTagged(elsewhere.Process.Pid)

	NukeTagged(root, false, SweepOptions{Scope: plantedOnly})
	if pidAlive(mineHere.Process.Pid) {
		t.Errorf("this checkout's tagged process %d survived", mineHere.Process.Pid)
	}
	if !pidAlive(elsewhere.Process.Pid) {
		t.Errorf("another checkout's process %d was killed without --any-checkout", elsewhere.Process.Pid)
	}

	NukeTagged(root, true, SweepOptions{Scope: plantedOnly})
	if pidAlive(elsewhere.Process.Pid) {
		t.Errorf("--any-checkout left tagged process %d alive", elsewhere.Process.Pid)
	}
}

// TestSweepSparesAnUntaggedProcessInTheSameGroup guards the group kill: a
// tagged process sharing a process group with an untagged one must die alone.
func TestSweepSparesAnUntaggedProcessInTheSameGroup(t *testing.T) {
	// The untagged sleeper leads its own group; the tagged one joins it.
	sleeper := startHelperIn(t, "", "", -1)
	tagged := startHelperIn(t, testRunPrefix+"samegroup", "/checkout/a", sleeper.Process.Pid)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		seen := false
		for _, p := range TaggedProcesses() {
			if p.PID == tagged.Process.Pid {
				seen = true
			}
		}
		if seen {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if g, _ := syscall.Getpgid(tagged.Process.Pid); g != sleeper.Process.Pid {
		t.Fatalf("setup: tagged pgid = %d, want sleeper's %d", g, sleeper.Process.Pid)
	}

	SweepTagged("test", func(tp Tagged) bool { return tp.RunID == testRunPrefix+"samegroup" },
		SweepOptions{Timeout: 2 * time.Second, Scope: plantedOnly})

	if pidAlive(tagged.Process.Pid) {
		t.Errorf("tagged helper %d survived", tagged.Process.Pid)
	}
	if !pidAlive(sleeper.Process.Pid) {
		t.Errorf("untagged helper %d in the same group was killed", sleeper.Process.Pid)
	}
}
