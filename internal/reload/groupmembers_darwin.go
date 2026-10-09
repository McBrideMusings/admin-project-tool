//go:build darwin

package reload

import "golang.org/x/sys/unix"

// procStateIdle is XNU's p_stat SIDL (sys/proc.h): still being created by
// fork.
const procStateIdle = 1

// groupMembers maps each pid in process group pgid to whether it is still
// being created by fork. ok is false when the group could not be read.
func groupMembers(pgid int) (members map[int]bool, ok bool) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return nil, false
	}
	members = make(map[int]bool, len(procs))
	for _, p := range procs {
		members[int(p.Proc.P_pid)] = p.Proc.P_stat == procStateIdle
	}
	return members, true
}
