"""App Store Connect API — enough of it to stop the developer portal being a
manual step.

Why this exists: registering an App ID and ticking a capability are the two
things that reliably stop an otherwise-automatable Apple build, and both are
API calls. Doing them by hand also hides the failure — an identifier that
exists but lacks a capability produces a build error naming the capability, so
the portal is where you go to check something you cannot see from here.

**No third-party dependencies, on purpose.** The tool runs on whatever python3
a machine happens to have, and neither PyJWT nor `cryptography` is present on
this one. ES256 is therefore signed with `openssl`, which every macOS has, and
the DER signature it returns is converted to the raw r||s pair JWS requires.
That conversion is the only fiddly part and it is tested.

Credentials come from the environment, never from a manifest — see
`apple.app_store_connect_auth_flags()` for the same three variables:

    APP_STORE_CONNECT_KEY_PATH    path to AuthKey_<KEYID>.p8
    APP_STORE_CONNECT_KEY_ID      the <KEYID> from that filename
    APP_STORE_CONNECT_ISSUER_ID   UUID from Users and Access ▸ Integrations
"""
from __future__ import annotations

import base64
import json
import os
import subprocess
import time
import urllib.error
import urllib.request

API_ROOT = "https://api.appstoreconnect.apple.com/v1"

# Apple's own enum. Only the ones worth reaching for from here; the API accepts
# any of its documented values and this list is not a whitelist, just the set
# that has a plain-language name in --capability.
CAPABILITY_ALIASES = {
    "icloud": "ICLOUD",
    "push": "PUSH_NOTIFICATIONS",
    "app-groups": "APP_GROUPS",
    "associated-domains": "ASSOCIATED_DOMAINS",
    "keychain-sharing": "KEYCHAIN_SHARING",
    "background-modes": "BACKGROUND_MODES",
    "sign-in-with-apple": "APPLE_ID_AUTH",
}


class AppStoreConnectError(RuntimeError):
    pass


def credentials():
    """(key_path, key_id, issuer_id), or None when the machine has no key.

    All three or nothing, exactly as the xcodebuild flags require — a partial
    credential is a confusing failure rather than a working half.
    """
    path = os.environ.get("APP_STORE_CONNECT_KEY_PATH", "").strip()
    key_id = os.environ.get("APP_STORE_CONNECT_KEY_ID", "").strip()
    issuer = os.environ.get("APP_STORE_CONNECT_ISSUER_ID", "").strip()
    if not (path and key_id and issuer):
        return None
    expanded = os.path.expanduser(os.path.expandvars(path))
    if not os.path.isfile(expanded):
        return None
    return expanded, key_id, issuer


def _b64(data: bytes) -> str:
    """base64url, unpadded — what JWS wants."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def der_to_raw_signature(der: bytes, size: int = 32) -> bytes:
    """Convert openssl's DER ECDSA signature to the raw r||s JWS expects.

    openssl emits `SEQUENCE { INTEGER r, INTEGER s }`; JWS wants the two values
    concatenated, each left-padded to the curve size. Getting this wrong
    produces a token Apple rejects as malformed with no hint as to which half
    of the process is at fault, which is why it is a named function with tests
    rather than four lines inline.
    """
    if not der or der[0] != 0x30:
        raise AppStoreConnectError("signature is not a DER SEQUENCE")
    # Length byte(s): short form, or long form with a leading 0x8n count.
    index = 2 if der[1] < 0x80 else 2 + (der[1] & 0x7F)

    def read_integer(pos):
        if der[pos] != 0x02:
            raise AppStoreConnectError("expected a DER INTEGER in the signature")
        length = der[pos + 1]
        value = der[pos + 2:pos + 2 + length]
        # DER keeps a leading zero byte to mark a positive number whose high
        # bit is set; the raw form does not want it.
        return value.lstrip(b"\x00"), pos + 2 + length

    r, next_pos = read_integer(index)
    s, _ = read_integer(next_pos)
    return r.rjust(size, b"\x00") + s.rjust(size, b"\x00")


def _sign_es256(message: bytes, key_path: str) -> bytes:
    result = subprocess.run(
        ["openssl", "dgst", "-sha256", "-sign", key_path],
        input=message, capture_output=True,
    )
    if result.returncode != 0:
        raise AppStoreConnectError(
            f"openssl could not sign with {key_path}: {result.stderr.decode(errors='replace').strip()}"
        )
    return der_to_raw_signature(result.stdout)


def token(now=None):
    """A bearer token for the API, good for 20 minutes.

    Apple caps these at 20 minutes and rejects anything longer, so there is no
    caching here worth having: a token is cheap and a stale one is a confusing
    401.
    """
    creds = credentials()
    if creds is None:
        raise AppStoreConnectError(
            "No App Store Connect API key configured. Set APP_STORE_CONNECT_KEY_PATH, "
            "APP_STORE_CONNECT_KEY_ID and APP_STORE_CONNECT_ISSUER_ID."
        )
    key_path, key_id, issuer = creds
    issued = int(now if now is not None else time.time())

    header = {"alg": "ES256", "kid": key_id, "typ": "JWT"}
    payload = {"iss": issuer, "iat": issued, "exp": issued + 20 * 60, "aud": "appstoreconnect-v1"}
    signing_input = f"{_b64(json.dumps(header).encode())}.{_b64(json.dumps(payload).encode())}".encode()
    return f"{signing_input.decode()}.{_b64(_sign_es256(signing_input, key_path))}"


def request(method, path, body=None, opener=None):
    """One API call. `path` is relative to /v1. Returns the parsed body, or {}
    for a 204."""
    url = path if path.startswith("http") else f"{API_ROOT}{path}"
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", f"Bearer {token()}")
    if data is not None:
        req.add_header("Content-Type", "application/json")

    send = opener or urllib.request.urlopen
    try:
        with send(req, timeout=30) as response:
            raw = response.read()
            return json.loads(raw) if raw else {}
    except urllib.error.URLError as error:
        # Only HTTPError carries a body worth unpacking. A plain URLError is no
        # DNS, no route, or the timeout above — and it used to escape as a
        # traceback, so a device deploy with the network down died before
        # xcodebuild ran instead of building against the cached profile.
        if not isinstance(error, urllib.error.HTTPError):
            raise AppStoreConnectError(f"{method} {path} → {error.reason}") from None
        detail = error.read().decode(errors="replace")
        try:
            errors = json.loads(detail).get("errors", [])
            detail = "; ".join(
                f"{e.get('title','')}: {e.get('detail','')}".strip(": ") for e in errors
            ) or detail
        except ValueError:
            pass
        raise AppStoreConnectError(f"{method} {path} → {error.code}: {detail}") from None
    except TimeoutError as error:
        raise AppStoreConnectError(f"{method} {path} → timed out: {error}") from None


def find_bundle_id(identifier, opener=None):
    """The API's resource for a bundle identifier, or None if unregistered.

    Filtered server-side rather than by walking every page: an account with a
    few hundred identifiers otherwise costs a dozen requests to answer one
    question.
    """
    result = request(
        "GET", f"/bundleIds?filter[identifier]={identifier}&limit=200", opener=opener
    )
    for item in result.get("data", []):
        if item.get("attributes", {}).get("identifier") == identifier:
            return item
    return None


def create_bundle_id(identifier, name, platform="UNIVERSAL", opener=None):
    """Register an identifier. `platform` is Apple's enum — IOS, MAC_OS or
    UNIVERSAL, and UNIVERSAL is what a single app across Mac, iPhone and Apple
    TV wants."""
    body = {
        "data": {
            "type": "bundleIds",
            "attributes": {"identifier": identifier, "name": name, "platform": platform},
        }
    }
    return request("POST", "/bundleIds", body=body, opener=opener).get("data", {})


def find_device(udid, opener=None):
    """The API's resource for a hardware UDID, or None if the team has never
    registered that device. Filtered server-side, like find_bundle_id."""
    result = request("GET", f"/devices?filter[udid]={udid}&limit=200", opener=opener)
    for item in result.get("data", []):
        if item.get("attributes", {}).get("udid") == udid:
            return item
    return None


def register_device(udid, name, platform="IOS", opener=None):
    """Add a device to the team so a development profile can include it.

    `platform` is Apple's BundleIdPlatform enum, which has no tvOS member —
    an Apple TV, an iPhone and an Apple Watch are all IOS devices here, and
    Apple derives the device class from the UDID itself.
    """
    body = {
        "data": {
            "type": "devices",
            "attributes": {"udid": udid, "name": name, "platform": platform},
        }
    }
    return request("POST", "/devices", body=body, opener=opener).get("data", {})


def enable_device(device_resource, opener=None):
    """Flip a DISABLED device back to ENABLED. A disabled device is still
    listed, so a plain existence check would call it registered while profiles
    keep leaving it out."""
    body = {
        "data": {
            "type": "devices",
            "id": device_resource,
            "attributes": {"status": "ENABLED"},
        }
    }
    return request("PATCH", f"/devices/{device_resource}", body=body, opener=opener).get("data", {})


def capabilities(bundle_id_resource, opener=None):
    """Which capabilities are already enabled, as Apple's enum values."""
    # No `limit` here: the API answers "The parameter 'limit' can not be used
    # with this request : This relationship does not support this parameter."
    # It is a relationship read, not a collection.
    result = request(
        "GET", f"/bundleIds/{bundle_id_resource}/bundleIdCapabilities", opener=opener
    )
    return [
        item.get("attributes", {}).get("capabilityType")
        for item in result.get("data", [])
    ]


def enable_capability(bundle_id_resource, capability_type, settings=None, opener=None):
    """Turn a capability on. Already-enabled is not an error worth raising —
    the caller wants the end state, not the transition."""
    body = {
        "data": {
            "type": "bundleIdCapabilities",
            "attributes": {"capabilityType": capability_type},
            "relationships": {
                "bundleId": {"data": {"type": "bundleIds", "id": bundle_id_resource}}
            },
        }
    }
    if settings:
        body["data"]["attributes"]["settings"] = settings
    return request("POST", "/bundleIdCapabilities", body=body, opener=opener).get("data", {})
