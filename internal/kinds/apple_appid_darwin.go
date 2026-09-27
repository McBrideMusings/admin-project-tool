//go:build darwin

// App Store Connect App ID management — appleEnsureDeviceRegistered and
// parseAppIDArgs, both of which call into internal/ascapi. Darwin-only for
// consistency with the rest of this package's Apple surface, though nothing
// here is actually macOS-specific beyond openssl being assumed present.
package kinds

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/ascapi"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// appleEnsureDeviceRegistered makes sure the team knows this device before a
// signed build asks Apple for a profile that has to include it.
//
// A device Apple has never seen fails the build with "Your team has no
// devices from which to generate a provisioning profile" — a message about
// the team, not about the device sitting on the desk, which is why plugging
// in a second phone or pairing an Apple TV used to mean a trip to the
// developer portal. -allowProvisioningUpdates creates profiles but never
// registers hardware, so the API key has to do it explicitly.
//
// Best-effort: with no API key, or on any API failure, this warns and
// returns so the build still runs and reports Apple's own error.
func appleEnsureDeviceRegistered(h *luahost.Host, udid, name string) {
	if udid == "" {
		return
	}
	creds := ascapi.LoadCredentials()
	if creds == nil {
		h.Warn("Skipping device registration — App Store Connect key " + ascapi.MissingCredentialReason())
		return
	}
	client := ascapi.NewClient(creds)
	existing, err := client.FindDevice(udid)
	if err != nil {
		h.Warn(fmt.Sprintf("Could not register %s with the team: %s", name, err))
		return
	}
	if existing == nil {
		h.Info(fmt.Sprintf("Registering %s (%s) with the team...", name, udid))
		if _, err := client.RegisterDevice(udid, name, "IOS"); err != nil {
			h.Warn(fmt.Sprintf("Could not register %s with the team: %s", name, err))
			return
		}
		h.Ok(name + " registered")
		return
	}
	if status, _ := existing["attributes"].(map[string]any)["status"].(string); status == "DISABLED" {
		id, _ := existing["id"].(string)
		h.Info(name + " is registered but disabled — re-enabling...")
		if _, err := client.EnableDevice(id); err != nil {
			h.Warn(fmt.Sprintf("Could not register %s with the team: %s", name, err))
			return
		}
		h.Ok(name + " enabled")
	}
}

// parseAppIDArgs splits `admin appid`'s leftover CLI args into the wanted
// capabilities (resolved through ascapi.CapabilityAliases, upper-cased when
// not a known alias), an optional --name display name, and the positional
// bundle identifiers.
func parseAppIDArgs(args []string) (wanted []string, displayName string, identifiers []string) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--capability" && i+1 < len(args):
			raw := args[i+1]
			if v, ok := ascapi.CapabilityAliases[strings.ToLower(raw)]; ok {
				wanted = append(wanted, v)
			} else {
				wanted = append(wanted, strings.ToUpper(raw))
			}
			i++
		case args[i] == "--name" && i+1 < len(args):
			displayName = args[i+1]
			i++
		default:
			identifiers = append(identifiers, args[i])
		}
	}
	return wanted, displayName, identifiers
}

// appleAppID implements `admin appid [<bundle-id>] [--capability NAME ...]
// [--name "Display"]`.
//
// Registers an App ID and turns capabilities on, so the developer portal
// stops being a step a person has to take between "the code is written" and
// "the build signs". With no bundle id it reports on the ones this project
// already builds, which is the question worth asking first — an identifier
// that exists but lacks a capability fails a build by naming the
// capability, not by naming itself.
func appleAppID(cfg map[string]any, h *luahost.Host, args []string) error {
	creds := ascapi.LoadCredentials()
	if creds == nil {
		h.Err("No App Store Connect API key configured.")
		h.Err("Set APP_STORE_CONNECT_KEY_PATH, APP_STORE_CONNECT_KEY_ID and")
		h.Err("APP_STORE_CONNECT_ISSUER_ID — the key comes from App Store Connect ▸")
		h.Err("Users and Access ▸ Integrations, and is downloadable exactly once.")
		return fmt.Errorf("appid: no App Store Connect API key configured")
	}

	wanted, displayName, identifiers := parseAppIDArgs(args)

	if len(identifiers) == 0 {
		// Whatever this project builds, both halves of the dev/release pair.
		project, err := appleProject(cfg, h)
		if err != nil {
			return err
		}
		for _, platform := range []string{"mac", "ios", "tv"} {
			for _, dev := range []bool{false, true} {
				found := appleBundle(project, cfg, platform, dev)
				if found != "" && !slices.Contains(identifiers, found) {
					identifiers = append(identifiers, found)
				}
			}
		}
		if len(identifiers) == 0 {
			return fmt.Errorf("no bundle identifiers found in this project, and none given")
		}
	}

	client := ascapi.NewClient(creds)
	failures := 0
	for _, identifier := range identifiers {
		resource, err := client.FindBundleID(identifier)
		if err != nil {
			h.Err(identifier + ": " + err.Error())
			failures++
			continue
		}
		if resource == nil {
			if len(wanted) == 0 && displayName == "" {
				h.Warn(identifier + ": not registered")
				continue
			}
			h.Info(identifier + ": registering")
			name := displayName
			if name == "" {
				name = identifier
			}
			resource, err = client.CreateBundleID(identifier, name, "")
			if err != nil {
				h.Err(identifier + ": " + err.Error())
				failures++
				continue
			}
		}
		resourceID, _ := resource["id"].(string)
		already, err := client.Capabilities(resourceID)
		if err != nil {
			h.Err(identifier + ": " + err.Error())
			failures++
			continue
		}
		for _, capability := range wanted {
			if slices.Contains(already, capability) {
				h.Info(identifier + ": " + capability + " already on")
				continue
			}
			if _, err := client.EnableCapability(resourceID, capability, nil); err != nil {
				h.Err(identifier + ": " + err.Error())
				failures++
				continue
			}
			h.Ok(identifier + ": " + capability + " enabled")
			already = append(already, capability)
		}
		sorted := append([]string(nil), already...)
		sort.Strings(sorted)
		list := "no capabilities"
		if len(sorted) > 0 {
			list = strings.Join(sorted, ", ")
		}
		h.Info(identifier + ": " + list)
	}
	if failures > 0 {
		return fmt.Errorf("appid: %d identifier(s) failed", failures)
	}
	return nil
}
