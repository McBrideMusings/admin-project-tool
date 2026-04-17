"""'apple' archetype — Xcode project with macOS and iOS targets.

Runtime env vars:
    APP_PROJECT    .xcodeproj or .xcworkspace path (default: inferred from pwd)
    APP_SCHEME     xcodebuild scheme name
    APP_CONFIG     build configuration (default: Debug)
    IOS_DEST       iOS destination spec (default: a generic simulator)
"""

ARCHETYPE = {
    "name": "apple",
    "modules": ["core", "icons", "apple"],
    "urls": {},
    "commands": {
        "build": {
            "kind": "multi-target",
            "desc": "Build for mac or ios",
            "targets": {
                "mac": (
                    "xcodebuild -project ${APP_PROJECT} -scheme ${APP_SCHEME} "
                    "-configuration ${APP_CONFIG:-Debug} "
                    "-destination 'platform=macOS' build"
                ),
                "ios": (
                    "xcodebuild -project ${APP_PROJECT} -scheme ${APP_SCHEME} "
                    "-configuration ${APP_CONFIG:-Debug} "
                    "-destination '${IOS_DEST:-generic/platform=iOS Simulator}' build"
                ),
            },
        },
        "dev": {
            "kind": "apple-dev-loop",
            "desc": "Build + run for mac or ios (dev loop)",
            "targets": {
                "mac": (
                    "xcodebuild -project ${APP_PROJECT} -scheme ${APP_SCHEME} "
                    "-configuration ${APP_CONFIG:-Debug} "
                    "-destination 'platform=macOS' build && "
                    "open $(find ~/Library/Developer/Xcode/DerivedData -name '${APP_SCHEME}.app' -type d | head -1)"
                ),
                "ios": (
                    "xcodebuild -project ${APP_PROJECT} -scheme ${APP_SCHEME} "
                    "-configuration ${APP_CONFIG:-Debug} "
                    "-destination '${IOS_DEST:-generic/platform=iOS Simulator}' build"
                ),
            },
        },
        "install": {
            "run": (
                "xcodebuild -project ${APP_PROJECT} -scheme ${APP_SCHEME} "
                "-configuration Release -destination 'generic/platform=iOS' install"
            ),
            "desc": "Build Release and install on connected device",
        },
        "clean": {
            "run": "rm -rf ~/Library/Developer/Xcode/DerivedData/${APP_SCHEME}-*",
            "desc": "Remove DerivedData for this scheme",
        },
    },
}
