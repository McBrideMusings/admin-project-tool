//go:build darwin

package luahost

import "testing"

const twoTargetSettings = `Build settings for action build and target Helper:
    FULL_PRODUCT_NAME = Helper.xpc
    PRODUCT_NAME = Helper
    TARGET_BUILD_DIR = /dd/Build/Products/Release/XPCServices

Build settings for action build and target App:
    FULL_PRODUCT_NAME = App.app
    PRODUCT_NAME = App
    TARGET_BUILD_DIR = /dd/Build/Products/Release
`

func TestPickTargetBuildDirMatchesProductBlock(t *testing.T) {
	if got, want := pickTargetBuildDir(twoTargetSettings, "App"), "/dd/Build/Products/Release"; got != want {
		t.Errorf("pickTargetBuildDir(App) = %q, want %q", got, want)
	}
}

func TestPickTargetBuildDirNoProductTakesFirst(t *testing.T) {
	if got, want := pickTargetBuildDir(twoTargetSettings, ""), "/dd/Build/Products/Release/XPCServices"; got != want {
		t.Errorf("pickTargetBuildDir(\"\") = %q, want %q", got, want)
	}
}

func TestPickTargetBuildDirUnmatchedProductFallsBackToFirst(t *testing.T) {
	if got, want := pickTargetBuildDir(twoTargetSettings, "Other"), "/dd/Build/Products/Release/XPCServices"; got != want {
		t.Errorf("pickTargetBuildDir(Other) = %q, want %q", got, want)
	}
}
