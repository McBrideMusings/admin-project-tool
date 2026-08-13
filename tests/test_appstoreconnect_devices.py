"""Device registration against App Store Connect.

The failure this covers, in full: a signed build for a real iPhone or Apple TV
asks Apple to make a development provisioning profile, and a profile can only
list devices the team has registered. Pair a brand-new Apple TV and the build
stops with "Your team has no devices from which to generate a provisioning
profile" — a sentence about the team, so it reads as an account problem rather
than as "Apple has never been told about the box on your desk".
`-allowProvisioningUpdates` does not help: it creates profiles, it never
registers hardware. So deploy registers the device itself, over the API.
"""
from __future__ import annotations

import json
import io

from admin_lib import appstoreconnect as asc


class _FakeResponse(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.close()
        return False


def _opener(routes, calls):
    """Answer requests from `routes` ({(method, path-substring): payload}) and
    record each one in `calls`."""
    def send(req, timeout=None):
        calls.append((req.get_method(), req.full_url,
                      json.loads(req.data.decode()) if req.data else None))
        for (method, fragment), payload in routes.items():
            if req.get_method() == method and fragment in req.full_url:
                return _FakeResponse(json.dumps(payload).encode())
        raise AssertionError(f"unexpected request: {req.get_method()} {req.full_url}")
    return send


def _no_token(monkeypatch):
    monkeypatch.setattr(asc, "token", lambda now=None: "fake-token")


TV_UDID = "00008110-000E6860012A401E"


def test_find_device_returns_none_for_an_unregistered_apple_tv(monkeypatch):
    _no_token(monkeypatch)
    calls = []
    opener = _opener({("GET", "/devices"): {"data": []}}, calls)
    assert asc.find_device(TV_UDID, opener=opener) is None
    assert f"filter[udid]={TV_UDID}" in calls[0][1], calls
    print("OK: an unregistered Apple TV comes back as None, filtered server-side")


def test_find_device_matches_on_the_exact_udid(monkeypatch):
    _no_token(monkeypatch)
    payload = {"data": [{"id": "DEV1", "attributes": {"udid": TV_UDID, "name": "Office"}}]}
    device = asc.find_device(TV_UDID, opener=_opener({("GET", "/devices"): payload}, []))
    assert device["id"] == "DEV1", device
    print("OK: a registered device is returned by UDID")


def test_register_device_posts_the_ios_platform_for_an_apple_tv(monkeypatch):
    _no_token(monkeypatch)
    calls = []
    opener = _opener({("POST", "/devices"): {"data": {"id": "DEV2"}}}, calls)
    asc.register_device(TV_UDID, "Office", opener=opener)
    body = calls[0][2]["data"]
    assert body["type"] == "devices", body
    # Apple's BundleIdPlatform enum has no tvOS member — an Apple TV registers
    # as IOS and Apple reads the device class off the UDID.
    assert body["attributes"] == {"udid": TV_UDID, "name": "Office", "platform": "IOS"}, body
    print("OK: an Apple TV registers under the IOS platform enum")


def test_enable_device_patches_status_back_to_enabled(monkeypatch):
    _no_token(monkeypatch)
    calls = []
    opener = _opener({("PATCH", "/devices/DEV3"): {"data": {"id": "DEV3"}}}, calls)
    asc.enable_device("DEV3", opener=opener)
    assert calls[0][2]["data"]["attributes"] == {"status": "ENABLED"}, calls[0][2]
    print("OK: a disabled device is re-enabled rather than treated as registered")
