package service

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/remotehost"
)

// remoteInstaller is the portable POSIX-sh installer pushed to the remote host
// and run there. It duplicates a small slice of the plist/unit rendering logic
// in shell rather than importing this package, on purpose: the remote host is
// not assumed to have Python, Go, or the admin tool installed at all.
//
//go:embed resources/install_service_remote.sh
var remoteInstaller string

// archMap turns the remote host's `uname -sm` into the dist matrix's
// <os>-<arch> filename suffix.
var archMap = map[string]string{
	"Darwin arm64":  "darwin-arm64",
	"Darwin x86_64": "darwin-amd64",
	"Linux x86_64":  "linux-amd64",
	"Linux aarch64": "linux-arm64",
}

// DeployRemote pushes a [go_dist]-built binary for host's architecture, plus
// the portable installer, and runs it there over SSH — installing the same
// [launchd] service remotely with no Python or the admin tool required on the
// remote host.
//
// distDir/binary match the [go_dist] config that built the matrix
// (binary-<os>-<arch> files plus VERSION); host is user@host or a bare host.
func DeployRemote(raw map[string]any, host, distDir, binary string, r Reporter) error {
	if remotehost.IsLocalHost(host) {
		return fmt.Errorf("host %s resolves to this machine — use `admin service install` instead", host)
	}
	c, err := ReadConfigForHost(raw, host, r)
	if err != nil {
		return err
	}

	unameOut, err := exec.Command("ssh", host, "uname -sm").Output()
	if err != nil {
		return fmt.Errorf("cannot ssh to %s: %w", host, err)
	}
	uname := strings.TrimSpace(string(unameOut))
	rkey, ok := archMap[uname]
	if !ok {
		return fmt.Errorf("unsupported host platform: %q", uname)
	}

	srcBin := filepath.Join(distDir, binary+"-"+rkey)
	if fi, err := os.Stat(srcBin); err != nil || fi.IsDir() {
		return fmt.Errorf("no bundled binary for %s (%s) — build the dist matrix first", rkey, srcBin)
	}

	remoteDir := ".admin-service/" + c.Label
	installerPath, cleanup, err := writeTempInstaller()
	if err != nil {
		return err
	}
	defer cleanup()

	if err := ssh(host, "mkdir -p "+remoteDir); err != nil {
		return fmt.Errorf("could not create %s on %s: %w", remoteDir, host, err)
	}
	// scp to a temp name and rename, for the same reason installBinary does on
	// the local path: the remote daemon may be running from that path, and
	// writing through its inode corrupts the running image.
	if err := scp(srcBin, host+":"+remoteDir+"/"+binary+".new"); err != nil {
		return fmt.Errorf("scp of the binary to %s failed: %w", host, err)
	}
	if err := scp(installerPath, host+":"+remoteDir+"/install_service_remote.sh"); err != nil {
		return fmt.Errorf("scp of the installer to %s failed: %w", host, err)
	}
	if err := ssh(host, fmt.Sprintf(
		"chmod +x %s/%s.new %s/install_service_remote.sh && mv -f %s/%s.new %s/%s",
		remoteDir, binary, remoteDir, remoteDir, binary, remoteDir, binary)); err != nil {
		return fmt.Errorf("installing the binary on %s failed: %w", host, err)
	}

	// $HOME is expanded by the REMOTE shell (this string travels as one ssh
	// argv element, no local shell involved) — literal, not locally quoted.
	flags := []string{"--label", shQuote(c.Label), "--bin", "$HOME/" + remoteDir + "/" + binary}
	for _, a := range c.Args {
		flags = append(flags, "--arg", shQuote(a))
	}
	for _, k := range sortedKeys(c.Env) {
		flags = append(flags, "--env", shQuote(k+"="+c.Env[k]))
	}
	flags = append(flags,
		"--stdout", "$HOME/"+remoteDir+"/run.log",
		"--stderr", "$HOME/"+remoteDir+"/run.log")

	if err := ssh(host, "bash "+remoteDir+"/install_service_remote.sh "+strings.Join(flags, " ")); err != nil {
		return fmt.Errorf("remote install on %s failed: %w", host, err)
	}
	r.Ok("installed " + c.Label + " on " + host)
	return nil
}

// UninstallRemote removes the [launchd] service on a remote host over SSH. No
// binary to push — just the portable installer, run with --uninstall.
func UninstallRemote(raw map[string]any, host string, r Reporter) error {
	if remotehost.IsLocalHost(host) {
		return fmt.Errorf("host %s resolves to this machine — use `admin service uninstall` instead", host)
	}
	c, err := ReadConfig(raw, r)
	if err != nil {
		return err
	}
	remoteDir := ".admin-service/" + c.Label
	installerPath, cleanup, err := writeTempInstaller()
	if err != nil {
		return err
	}
	defer cleanup()

	if err := ssh(host, "mkdir -p "+remoteDir); err != nil {
		return fmt.Errorf("could not create %s on %s: %w", remoteDir, host, err)
	}
	if err := scp(installerPath, host+":"+remoteDir+"/install_service_remote.sh"); err != nil {
		return fmt.Errorf("scp of the installer to %s failed: %w", host, err)
	}
	if err := ssh(host, fmt.Sprintf(
		"chmod +x %s/install_service_remote.sh && bash %s/install_service_remote.sh --label %s --uninstall",
		remoteDir, remoteDir, shQuote(c.Label))); err != nil {
		return fmt.Errorf("remote uninstall on %s failed: %w", host, err)
	}
	r.Ok("uninstalled " + c.Label + " on " + host)
	return nil
}

// writeTempInstaller materialises the embedded installer so scp has a real
// path to send. The Python original scp'd it straight out of the package
// directory; a Go binary carries it inside itself instead.
func writeTempInstaller() (string, func(), error) {
	f, err := os.CreateTemp("", "install_service_remote-*.sh")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := f.WriteString(remoteInstaller); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func ssh(host, cmd string) error {
	c := exec.Command("ssh", host, cmd)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func scp(src, dest string) error {
	c := exec.Command("scp", "-q", src, dest)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
