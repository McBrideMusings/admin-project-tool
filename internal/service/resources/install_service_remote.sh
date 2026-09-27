#!/bin/sh
# install_service_remote.sh — install a binary as an always-on user service
# ON THIS MACHINE (launchd on macOS, systemd --user on Linux). Portable POSIX
# sh with no dependency on Go or the admin tool being installed here — pushed
# and run on a remote host by internal/service's DeployRemote, which has
# already scp'd the binary into place before invoking this script.
#
#   install_service_remote.sh --label L --bin PATH
#       [--arg A]... [--env KEY=VAL]... [--stdout PATH] [--stderr PATH]
#       [--workdir DIR]
#   install_service_remote.sh --label L --uninstall
#
# No `set -e`: each step is checked explicitly so a failure says what broke.
set -u

label=""; bin=""; workdir=""; stdout_path=""; stderr_path=""; uninstall=0
args=""; envs=""

die() { echo "install-service-remote: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --label) label="$2"; shift 2 ;;
    --bin) bin="$2"; shift 2 ;;
    --arg) args="$args$2 "; shift 2 ;;
    --env) envs="$envs$2 "; shift 2 ;;
    --stdout) stdout_path="$2"; shift 2 ;;
    --stderr) stderr_path="$2"; shift 2 ;;
    --workdir) workdir="$2"; shift 2 ;;
    --uninstall) uninstall=1; shift ;;
    *) die "unknown flag: $1" ;;
  esac
done

[ -n "$label" ] || die "no --label given"

os="$(uname -s)"

case "$os" in
Darwin)
  plist="$HOME/Library/LaunchAgents/$label.plist"
  uid="$(id -u)"
  if [ "$uninstall" = 1 ]; then
    launchctl bootout "gui/$uid/$label" 2>/dev/null || true
    rm -f "$plist"
    echo "uninstalled $label (launchd)"
    exit 0
  fi
  [ -n "$bin" ] || die "no --bin given"
  [ -f "$bin" ] || die "binary not found at $bin"
  chmod +x "$bin"

  args_xml=""
  for a in $args; do
    args_xml="$args_xml    <string>$a</string>
"
  done
  env_xml=""
  if [ -n "$envs" ]; then
    env_xml="  <key>EnvironmentVariables</key>
  <dict>
"
    for kv in $envs; do
      k="${kv%%=*}"; v="${kv#*=}"
      env_xml="$env_xml    <key>$k</key><string>$v</string>
"
    done
    env_xml="$env_xml  </dict>
"
  fi
  workdir_xml=""
  [ -n "$workdir" ] && workdir_xml="  <key>WorkingDirectory</key><string>$workdir</string>
"
  out_xml=""
  [ -n "$stdout_path" ] && out_xml="  <key>StandardOutPath</key><string>$stdout_path</string>
"
  err_xml=""
  [ -n "$stderr_path" ] && err_xml="  <key>StandardErrorPath</key><string>$stderr_path</string>
"

  mkdir -p "$HOME/Library/LaunchAgents"
  # STOP before bootstrapping: reloading an already-loaded label with the
  # same plist path fails with "already loaded", and if the binary just
  # changed the old process must not keep serving from the old image.
  launchctl bootout "gui/$uid/$label" 2>/dev/null || true
  for _ in $(seq 1 25); do launchctl print "gui/$uid/$label" >/dev/null 2>&1 || break; sleep 0.2; done

  cat > "$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key>
  <array>
    <string>$bin</string>
$args_xml  </array>
$env_xml$workdir_xml$out_xml$err_xml  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
PLIST
  launchctl bootstrap "gui/$uid" "$plist" || die "launchctl could not load $plist"
  echo "installed $label (launchd, KeepAlive)"
  ;;
Linux)
  unit_dir="$HOME/.config/systemd/user"
  unit="$unit_dir/$label.service"
  if [ "$uninstall" = 1 ]; then
    systemctl --user disable --now "$label.service" 2>/dev/null || true
    rm -f "$unit"
    systemctl --user daemon-reload 2>/dev/null || true
    echo "uninstalled $label.service (systemd --user)"
    exit 0
  fi
  [ -n "$bin" ] || die "no --bin given"
  [ -f "$bin" ] || die "binary not found at $bin"
  chmod +x "$bin"

  exec_line="$bin"
  for a in $args; do exec_line="$exec_line $a"; done

  mkdir -p "$unit_dir"
  {
    echo "[Unit]"
    echo "Description=$label"
    echo "After=network-online.target"
    echo ""
    echo "[Service]"
    echo "ExecStart=$exec_line"
    for kv in $envs; do echo "Environment=$kv"; done
    [ -n "$workdir" ] && echo "WorkingDirectory=$workdir"
    [ -n "$stdout_path" ] && echo "StandardOutput=append:$stdout_path"
    [ -n "$stderr_path" ] && echo "StandardError=append:$stderr_path"
    echo "Restart=always"
    echo "RestartSec=5"
    echo ""
    echo "[Install]"
    echo "WantedBy=default.target"
  } > "$unit"
  systemctl --user daemon-reload || die "systemctl daemon-reload failed"
  systemctl --user enable --now "$label.service" || die "systemctl enable --now failed"
  command -v loginctl >/dev/null 2>&1 && loginctl enable-linger "$(id -un)" 2>/dev/null || true
  echo "installed $label.service (systemd --user, Restart=always)"
  ;;
*)
  die "unsupported OS: $os (macOS launchd / Linux systemd only)"
  ;;
esac
