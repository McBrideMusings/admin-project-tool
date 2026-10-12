//go:build darwin

package luahost

import "testing"

const simListing = `== Devices ==
-- iOS 18.2 --
    iPhone 16 Pro (11111111-1111-1111-1111-111111111111) (Shutdown)
    iPad Air (22222222-2222-2222-2222-222222222222) (Booted)
-- iOS 26.0 --
    iPhone 16 Pro (33333333-3333-3333-3333-333333333333) (Shutdown)
`

func TestFindSimulatorDevice(t *testing.T) {
	orig := execOutput
	t.Cleanup(func() { execOutput = orig })
	execOutput = func(string, ...string) (string, error) { return simListing, nil }

	cases := []struct{ device, id, name string }{
		{"iPhone 16 Pro", "33333333-3333-3333-3333-333333333333", "iPhone 16 Pro"}, // newest runtime
		{"iPad Air", "22222222-2222-2222-2222-222222222222", "iPad Air"},           // booted
		{"11111111-1111-1111-1111-111111111111", "11111111-1111-1111-1111-111111111111", "iPhone 16 Pro"},
		{"44444444-4444-4444-4444-444444444444", "", ""}, // unknown UDID
		{"iPhone 16", "", ""}, // exact name only
	}
	for _, c := range cases {
		id, name, err := FindSimulatorDevice(c.device)
		if err != nil || id != c.id || name != c.name {
			t.Errorf("FindSimulatorDevice(%q) = (%q, %q, %v), want (%q, %q)", c.device, id, name, err, c.id, c.name)
		}
	}
}
