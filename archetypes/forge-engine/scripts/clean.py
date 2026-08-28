
import shutil
_removed = 0
for _p in ("engine/zig-cache", "engine/zig-out"):
    if os.path.exists(_p):
        info("rm -rf " + _p)
        shutil.rmtree(_p)
        _removed += 1
if _removed:
    ok("clean done")
else:
    info("nothing to clean")
