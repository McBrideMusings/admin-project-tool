//go:build darwin

package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/simpool"
)

var simUDIDArg = regexp.MustCompile(`^[A-Fa-f0-9]{8}-([A-Fa-f0-9]{4}-){3}[A-Fa-f0-9]{12}$`)

// ownLeaseToCaller makes a pool lease belong to the process that ran admin
// rather than to admin itself: `sim launch` exits while the app keeps running,
// and a lease owned by admin would shut the device down with it. Inside a
// Claude Code session the pool already keys the lease to that session.
func ownLeaseToCaller() {
	if os.Getenv("CLAUDE_PID") == "" && os.Getenv("SIM_POOL_OWNER") == "" {
		_ = os.Setenv("SIM_POOL_OWNER", strconv.Itoa(os.Getppid()))
	}
}

var (
	simLaunchDevice   string
	simLaunchPlatform string
)

// simCmd groups simulator verbs a script can call without an admin.toml —
// the same luahost helpers a `kind = "lua"` action reaches as apple.*.
var simCmd = &cobra.Command{
	Use:   "sim",
	Short: "Drive the iOS/tvOS simulator with the same helpers admin's apple actions use",
}

var simLaunchCmd = &cobra.Command{
	Use:   "launch <app>",
	Short: "Resolve and boot a simulator, then install and launch a built .app on it",
	Long: `Read the built .app's CFBundleIdentifier, resolve a simulator, wait for it
to boot, install the app and launch it. Needs no admin.toml.

Without --device the simulator is the one apple.ensure_booted_simulator picks:
an already-booted device of --platform, else the newest available one.
--device takes a UDID or an exact device name ("iPhone 16 Pro"); a name on
several runtimes picks the booted one, else the newest runtime.

The last line on stdout is "<bundle-id> <udid> <device name>" for the caller.
Exits non-zero when the bundle id, the simulator, the install or the launch
fails.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (rerr error) {
		// A failed launch frees the lease; a successful one keeps it because
		// the app is still running.
		defer func() {
			if rerr != nil {
				_ = simpool.ReleaseHeld()
			}
		}()
		app := args[0]
		bundle := luahost.AppBundleIdentifier(app)
		if bundle == "" {
			return fmt.Errorf("could not read CFBundleIdentifier from %s/Info.plist", app)
		}

		ownLeaseToCaller()
		var udid, name string
		leased := false
		if !simUDIDArg.MatchString(simLaunchDevice) {
			id, n, info, ok, err := luahost.LeasePoolSimulator(simLaunchPlatform, simLaunchDevice)
			if err != nil {
				return err
			}
			if ok {
				leased = true
				fmt.Fprintln(cmd.ErrOrStderr(), info)
				udid, name = id, n
				luahost.WaitBootedSimulator(udid)
			}
		}
		if leased {
			// the pool's device is already booted and named above
		} else if simLaunchDevice == "" {
			id, n, info, err := luahost.EnsureBootedSimulator(simLaunchPlatform)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), info)
			udid, name = id, n
		} else {
			id, n, err := luahost.FindSimulatorDevice(simLaunchDevice)
			if err != nil {
				return err
			}
			if id == "" {
				return fmt.Errorf("no available simulator named or with UDID %q", simLaunchDevice)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Using simulator: %s\n", n)
			udid, name = id, n
			luahost.WaitBootedSimulator(udid)
		}

		fmt.Fprintf(cmd.ErrOrStderr(), "Installing %s on %s (%s)\n", bundle, name, udid)
		if err := luahost.InstallAndLaunchSim(udid, app, bundle, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", bundle, udid, name)
		return nil
	},
}

// simPoolPin is the device-type name to lease for --device: empty when
// --device is unset or already a UDID (a UDID names a device, not a type).
func simPoolPin(device string) string {
	if simUDIDArg.MatchString(device) {
		return ""
	}
	return device
}

var (
	simRunDevice   string
	simRunPlatform string
)

var simRunCmd = &cobra.Command{
	Use:   "run [flags] -- <command> [args...]",
	Short: "Run a command with a leased, booted simulator; release it when the command ends",
	Long: `Take a simulator, run the command with its UDID in ADMIN_SIM_UDID, then give
the simulator back — on success, on failure and on Ctrl-C. This is the door for
a test or screenshot script that drives a simulator admin does not pick itself.

With the simulator pool configured (ADMIN_SIM_TOOL) the device comes from the
pool; without it, the booted simulator of --platform, else the newest one.
Exits non-zero when the command does.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		defer func() {
			if err := simpool.ReleaseHeld(); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
			}
		}()
		var udid string
		if id, _, info, leased, err := luahost.LeasePoolSimulator(simRunPlatform, simPoolPin(simRunDevice)); err != nil {
			return err
		} else if leased {
			fmt.Fprintln(cmd.ErrOrStderr(), info)
			udid = id
			luahost.WaitBootedSimulator(udid)
		} else {
			id, _, info, err := luahost.EnsureBootedSimulator(simRunPlatform)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), info)
			udid = id
		}
		// Ctrl-C reaches the child through the terminal's process group; admin
		// ignores it so the deferred release still runs once the child ends.
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigs)
		child := exec.Command(args[0], args[1:]...)
		child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
		child.Env = append(os.Environ(), "ADMIN_SIM_UDID="+udid)
		err := child.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			cmd.SilenceUsage = true
			return fmt.Errorf("%s exited %d", args[0], ee.ExitCode())
		}
		return err
	},
}

func init() {
	simRunCmd.Flags().StringVar(&simRunDevice, "device", "", "device-type name to lease from the pool (default: the pool's)")
	simRunCmd.Flags().StringVar(&simRunPlatform, "platform", "ios", "simulator platform: ios or tv")
	simCmd.AddCommand(simRunCmd)
	simLaunchCmd.Flags().StringVar(&simLaunchDevice, "device", "", "simulator UDID or exact device name (default: auto-resolve)")
	simLaunchCmd.Flags().StringVar(&simLaunchPlatform, "platform", "ios", "simulator platform to auto-resolve: ios or tv")
	simCmd.AddCommand(simLaunchCmd)
	rootCmd.AddCommand(simCmd)
}
