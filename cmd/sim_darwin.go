//go:build darwin

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

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
	RunE: func(cmd *cobra.Command, args []string) error {
		app := args[0]
		bundle := luahost.AppBundleIdentifier(app)
		if bundle == "" {
			return fmt.Errorf("could not read CFBundleIdentifier from %s/Info.plist", app)
		}

		var udid, name string
		if simLaunchDevice == "" {
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

func init() {
	simLaunchCmd.Flags().StringVar(&simLaunchDevice, "device", "", "simulator UDID or exact device name (default: auto-resolve)")
	simLaunchCmd.Flags().StringVar(&simLaunchPlatform, "platform", "ios", "simulator platform to auto-resolve: ios or tv")
	simCmd.AddCommand(simLaunchCmd)
	rootCmd.AddCommand(simCmd)
}
