pid_file = "/tmp/admin-run.pid"
if not os.path.exists(pid_file):
    err("no admin dev process found (no PID file at " + pid_file + ")"); sys.exit(1)
with open(pid_file) as f:
    pid = int(f.read().strip())
try:
    os.kill(pid, signal.SIGUSR1)
except ProcessLookupError:
    err("PID " + str(pid) + " is not running (stale PID file)")
    os.remove(pid_file); sys.exit(1)
ok("reload signal sent to PID " + str(pid))
