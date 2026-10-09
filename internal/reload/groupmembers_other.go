//go:build !darwin && !windows

package reload

// groupMembers reports ok=false off darwin, so killAndWait sends one plain
// group signal. On Linux, copy_process in kernel/fork.c restarts a fork that
// a signal interrupts, so a group signal reaches a concurrently forked child
// and the race killAndWait's per-pid signalling closes does not occur.
// Whether the other BSDs share darwin's race is untested.
func groupMembers(int) (map[int]bool, bool) { return nil, false }
