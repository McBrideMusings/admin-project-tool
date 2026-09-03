// Package ascapi is the Go port of admin_lib/appstoreconnect.py — enough of
// the App Store Connect API to stop the developer portal being a manual
// step. Registering an App ID and ticking a capability are the two things
// that reliably stop an otherwise-automatable Apple build, and both are API
// calls: an identifier that exists but lacks a capability fails a build by
// naming the capability, not by naming itself, so the portal is where you'd
// otherwise go to check something invisible from here.
//
// No third-party dependencies, on purpose (ADR-0001, "stdlib only"). Go has
// no stdlib ES256 JWS signer either, so this signs with `openssl` — every
// macOS has it — exactly as the Python original did, and converts the DER
// signature openssl returns to the raw r||s pair JWS requires.
//
// Credentials come from the environment, never from a manifest — the same
// three variables internal/kinds' appleAuthArgv and
// internal/luahost.AppStoreConnectAuthFlags already read for xcodebuild:
//
//	APP_STORE_CONNECT_KEY_PATH    path to AuthKey_<KEYID>.p8
//	APP_STORE_CONNECT_KEY_ID      the <KEYID> from that filename
//	APP_STORE_CONNECT_ISSUER_ID   UUID from Users and Access ▸ Integrations
package ascapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// APIRoot is the App Store Connect API's base URL.
const APIRoot = "https://api.appstoreconnect.apple.com/v1"

// CapabilityAliases mirrors Apple's own enum — only the ones worth reaching
// for from --capability. Not a whitelist: the API accepts any of its
// documented values, and a name that isn't in this table is upper-cased and
// sent through unchanged.
var CapabilityAliases = map[string]string{
	"icloud":             "ICLOUD",
	"push":               "PUSH_NOTIFICATIONS",
	"app-groups":         "APP_GROUPS",
	"associated-domains": "ASSOCIATED_DOMAINS",
	// No "keychain-sharing" alias: confirmed via a live 409 that Apple's real
	// capabilityType enum has no keychain value at all, and a project's own
	// keychain-access-groups entitlement (the default self-owned group,
	// $(AppIdentifierPrefix)<bundle-id>) needs no App ID capability toggle —
	// a caller passing --capability keychain-sharing should get an honest
	// "not a real ASC capability" attempt and 409, not a maintained-looking
	// alias that always fails.
	"background-modes":   "BACKGROUND_MODES",
	"sign-in-with-apple": "APPLE_ID_AUTH",
}

// CapabilityDefaultSettings covers capabilities the API rejects without a
// settings array, so a caller asking only for the capability by name gets a
// working call instead of a 409 naming the missing setting.
//
// ICLOUD without a version fails with "cannot have the CloudkitVersion
// 'null'". XCODE_6 is CloudKit; XCODE_5 is the legacy documents/key-value
// store only. Keys are Apple's own: CapabilitySetting.key and
// CapabilityOption.key.
// https://developer.apple.com/documentation/appstoreconnectapi/capabilitysetting
var CapabilityDefaultSettings = map[string][]map[string]any{
	"ICLOUD": {
		{"key": "ICLOUD_VERSION", "options": []map[string]any{{"key": "XCODE_6"}}},
	},
}

// Credentials is the (key_path, key_id, issuer_id) triple xcodebuild's
// -authenticationKey* flags and this package's JWT signer both need.
type Credentials struct {
	KeyPath  string
	KeyID    string
	IssuerID string
}

// LoadCredentials reads the three env vars and returns nil when any is
// unset or the key file isn't there — all three or nothing, exactly as the
// xcodebuild flags require, since a partial credential is a confusing
// failure rather than a working half.
func LoadCredentials() *Credentials {
	path := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_PATH"))
	keyID := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_ID"))
	issuer := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_ISSUER_ID"))
	if path == "" || keyID == "" || issuer == "" {
		return nil
	}
	expanded := expandPath(path)
	if info, err := os.Stat(expanded); err != nil || info.IsDir() {
		return nil
	}
	return &Credentials{KeyPath: expanded, KeyID: keyID, IssuerID: issuer}
}

// MissingCredentialReason explains why LoadCredentials returned nil, in one
// line — "not configured" is the right message for unset variables and the
// wrong one for a key path with a typo in it: the key IS configured, it is
// the file that isn't there, and being told to configure what you already
// configured sends you to the wrong place.
func MissingCredentialReason() string {
	path := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_PATH"))
	keyID := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_ID"))
	issuer := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_ISSUER_ID"))
	var unset []string
	if path == "" {
		unset = append(unset, "APP_STORE_CONNECT_KEY_PATH")
	}
	if keyID == "" {
		unset = append(unset, "APP_STORE_CONNECT_KEY_ID")
	}
	if issuer == "" {
		unset = append(unset, "APP_STORE_CONNECT_ISSUER_ID")
	}
	if len(unset) > 0 {
		return "not configured — unset: " + strings.Join(unset, ", ")
	}
	expanded := expandPath(path)
	if info, err := os.Stat(expanded); err != nil || info.IsDir() {
		return "APP_STORE_CONNECT_KEY_PATH points at a file that is not there: " + expanded
	}
	return ""
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

func b64(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// derToRawSignature converts openssl's DER ECDSA signature to the raw r||s
// JWS expects.
//
// openssl emits `SEQUENCE { INTEGER r, INTEGER s }`; JWS wants the two
// values concatenated, each left-padded to the curve size. Getting this
// wrong produces a token Apple rejects as malformed with no hint as to
// which half of the process is at fault, which is why it is a named
// function with tests rather than a few lines inline.
func derToRawSignature(der []byte, size int) ([]byte, error) {
	if len(der) == 0 || der[0] != 0x30 {
		return nil, errors.New("signature is not a DER SEQUENCE")
	}
	// Length byte(s): short form, or long form with a leading 0x8n count.
	index := 2
	if der[1] >= 0x80 {
		index = 2 + int(der[1]&0x7F)
	}
	readInteger := func(pos int) ([]byte, int, error) {
		if pos+1 >= len(der) || der[pos] != 0x02 {
			return nil, 0, errors.New("expected a DER INTEGER in the signature")
		}
		length := int(der[pos+1])
		end := pos + 2 + length
		if end > len(der) {
			return nil, 0, errors.New("truncated DER INTEGER in the signature")
		}
		value := der[pos+2 : end]
		// DER keeps a leading zero byte to mark a positive number whose high
		// bit is set; the raw form does not want it.
		value = bytes.TrimLeft(value, "\x00")
		return value, end, nil
	}
	r, next, err := readInteger(index)
	if err != nil {
		return nil, err
	}
	s, _, err := readInteger(next)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, size*2)
	out = append(out, leftPad(r, size)...)
	out = append(out, leftPad(s, size)...)
	return out, nil
}

// leftPad mirrors Python's bytes.rjust(size, b"\x00") — pads on the left
// with zero bytes, and leaves b untouched (no truncation) when it is
// already size or longer.
func leftPad(b []byte, size int) []byte {
	if len(b) >= size {
		return b
	}
	out := make([]byte, size-len(b), size)
	return append(out, b...)
}

func signES256(message []byte, keyPath string) ([]byte, error) {
	cmd := exec.Command("openssl", "dgst", "-sha256", "-sign", keyPath)
	cmd.Stdin = bytes.NewReader(message)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("openssl could not sign with %s: %s", keyPath, strings.TrimSpace(stderr.String()))
	}
	return derToRawSignature(stdout.Bytes(), 32)
}

// Token returns a bearer token for the API, good for 20 minutes — Apple
// caps these and rejects anything longer, so there is no caching here worth
// having: a token is cheap and a stale one is a confusing 401.
func Token(creds *Credentials, now time.Time) (string, error) {
	if creds == nil {
		return "", errors.New("No App Store Connect API key configured. Set APP_STORE_CONNECT_KEY_PATH, " +
			"APP_STORE_CONNECT_KEY_ID and APP_STORE_CONNECT_ISSUER_ID.")
	}
	issued := now.Unix()
	header := map[string]any{"alg": "ES256", "kid": creds.KeyID, "typ": "JWT"}
	payload := map[string]any{
		"iss": creds.IssuerID, "iat": issued, "exp": issued + 20*60, "aud": "appstoreconnect-v1",
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	signingInput := b64(hb) + "." + b64(pb)
	sig, err := signES256([]byte(signingInput), creds.KeyPath)
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64(sig), nil
}

// Client is one authenticated App Store Connect API session. The zero value
// is not usable — build one with NewClient.
type Client struct {
	Creds      *Credentials
	HTTPClient *http.Client
	Now        func() time.Time

	mu          sync.Mutex
	cachedToken string
	cachedExp   time.Time
}

// NewClient builds a Client with sane defaults (http.DefaultClient,
// time.Now) — override HTTPClient/Now in a test to stand in a fake
// transport or a fixed clock.
func NewClient(creds *Credentials) *Client {
	return &Client{Creds: creds, HTTPClient: http.DefaultClient, Now: time.Now}
}

// tokenTTL is how long a minted token is reused before Client mints a new
// one. Apple issues these good for 20 minutes; this stays well inside that
// so a token is never sent past its `exp`.
const tokenTTL = 15 * time.Minute

// token returns a cached bearer token, minting a fresh one only when there
// is none yet, force is set, or the cached one is past tokenTTL.
//
// This exists because of a real, reproduced bug: `iat` is second-resolution
// (`now.Unix()`), and Request used to mint a brand-new token — a brand-new
// `iat` — on every single call. Two calls issued within the same wall-clock
// second (FindBundleID immediately followed by Capabilities, the normal
// shape of a bare `admin appid <id>`) produced two different, individually
// valid tokens sharing the identical (kid, iat) pair. Apple's auth layer
// appears to treat the second token in such a pair as a replay and rejects
// it with 401 — reproduced by hand, 100% repeatable, against a real key:
// the *first* ASC request in a process always succeeded, every request
// after it in the same process failed, and an isolated one-shot script
// making a single request to the exact same endpoint always succeeded. A
// short retry-with-backoff (this file used to have one) papers over that
// symptom by hoping the clock ticks over before the retry's `iat` collides
// again; it does not fix the actual cause, which is minting more tokens
// than the API wants to see per second. One token, reused for the life of
// the Client, both fixes it and cuts request volume against a key ADR-0013
// already flagged as easy to rate-limit.
func (c *Client) token(force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	if !force && c.cachedToken != "" && now.Before(c.cachedExp) {
		return c.cachedToken, nil
	}
	tok, err := Token(c.Creds, now)
	if err != nil {
		return "", err
	}
	c.cachedToken = tok
	c.cachedExp = now.Add(tokenTTL)
	return tok, nil
}

// Request makes one API call. path is relative to /v1 unless it already
// looks like a full URL. Returns the parsed body, or {} for a 204.
//
// authRetries is how many times Request retries a 401 before giving up —
// a backstop for the rare case a cached token really has gone bad
// (revoked, clock skew) rather than the token-per-second collision `token`
// above exists to prevent. force=true on every retry so a bad cached token
// is never resent.
const authRetries = 3

// sleepBetweenAuthRetries is overridden by tests to skip the real delay.
var sleepBetweenAuthRetries = func(attempt int) { time.Sleep(time.Duration(attempt) * time.Second) }

func (c *Client) Request(method, path string, body any) (map[string]any, error) {
	url := path
	if !strings.HasPrefix(path, "http") {
		url = APIRoot + path
	}
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}

	var lastErr error
	for attempt := 1; attempt <= authRetries; attempt++ {
		var reader io.Reader
		if encoded != nil {
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequest(method, url, reader)
		if err != nil {
			return nil, err
		}
		tok, err := c.token(attempt > 1)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		client := c.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s %s → %s", method, path, err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("%s %s → %s", method, path, err)
		}

		if resp.StatusCode >= 400 {
			detail := string(raw)
			var parsed struct {
				Errors []struct {
					Title  string `json:"title"`
					Detail string `json:"detail"`
				} `json:"errors"`
			}
			if json.Unmarshal(raw, &parsed) == nil && len(parsed.Errors) > 0 {
				parts := make([]string, 0, len(parsed.Errors))
				for _, e := range parsed.Errors {
					parts = append(parts, strings.Trim(e.Title+": "+e.Detail, ": "))
				}
				detail = strings.Join(parts, "; ")
			}
			lastErr = fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, detail)
			if resp.StatusCode == 401 && attempt < authRetries {
				sleepBetweenAuthRetries(attempt)
				continue
			}
			return nil, lastErr
		}
		if len(raw) == 0 {
			return map[string]any{}, nil
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, lastErr
}

func dataList(result map[string]any) []map[string]any {
	raw, _ := result["data"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func attrString(item map[string]any, key string) (string, bool) {
	attrs, _ := item["attributes"].(map[string]any)
	if attrs == nil {
		return "", false
	}
	v, ok := attrs[key].(string)
	return v, ok
}

// FindBundleID returns the API's resource for a bundle identifier, or nil
// if unregistered. Filtered server-side rather than by walking every page:
// an account with a few hundred identifiers otherwise costs a dozen
// requests to answer one question.
func (c *Client) FindBundleID(identifier string) (map[string]any, error) {
	result, err := c.Request("GET", fmt.Sprintf("/bundleIds?filter[identifier]=%s&limit=200", identifier), nil)
	if err != nil {
		return nil, err
	}
	for _, item := range dataList(result) {
		if v, ok := attrString(item, "identifier"); ok && v == identifier {
			return item, nil
		}
	}
	return nil, nil
}

// CreateBundleID registers an identifier. platform is Apple's enum — IOS,
// MAC_OS or UNIVERSAL — and empty defaults to UNIVERSAL, what a single app
// across Mac, iPhone and Apple TV wants.
func (c *Client) CreateBundleID(identifier, name, platform string) (map[string]any, error) {
	if platform == "" {
		platform = "UNIVERSAL"
	}
	body := map[string]any{
		"data": map[string]any{
			"type":       "bundleIds",
			"attributes": map[string]any{"identifier": identifier, "name": name, "platform": platform},
		},
	}
	result, err := c.Request("POST", "/bundleIds", body)
	if err != nil {
		return nil, err
	}
	data, _ := result["data"].(map[string]any)
	return data, nil
}

// FindDevice returns the API's resource for a hardware UDID, or nil if the
// team has never registered that device. Filtered server-side, like
// FindBundleID.
func (c *Client) FindDevice(udid string) (map[string]any, error) {
	result, err := c.Request("GET", fmt.Sprintf("/devices?filter[udid]=%s&limit=200", udid), nil)
	if err != nil {
		return nil, err
	}
	for _, item := range dataList(result) {
		if v, ok := attrString(item, "udid"); ok && v == udid {
			return item, nil
		}
	}
	return nil, nil
}

// RegisterDevice adds a device to the team so a development profile can
// include it. platform is Apple's BundleIdPlatform enum, which has no tvOS
// member — an Apple TV, an iPhone and an Apple Watch are all IOS devices
// here, and Apple derives the device class from the UDID itself. Empty
// defaults to IOS.
func (c *Client) RegisterDevice(udid, name, platform string) (map[string]any, error) {
	if platform == "" {
		platform = "IOS"
	}
	body := map[string]any{
		"data": map[string]any{
			"type":       "devices",
			"attributes": map[string]any{"udid": udid, "name": name, "platform": platform},
		},
	}
	result, err := c.Request("POST", "/devices", body)
	if err != nil {
		return nil, err
	}
	data, _ := result["data"].(map[string]any)
	return data, nil
}

// EnableDevice flips a DISABLED device back to ENABLED. A disabled device
// is still listed, so a plain existence check would call it registered
// while profiles keep leaving it out.
func (c *Client) EnableDevice(deviceResource string) (map[string]any, error) {
	body := map[string]any{
		"data": map[string]any{
			"type":       "devices",
			"id":         deviceResource,
			"attributes": map[string]any{"status": "ENABLED"},
		},
	}
	result, err := c.Request("PATCH", "/devices/"+deviceResource, body)
	if err != nil {
		return nil, err
	}
	data, _ := result["data"].(map[string]any)
	return data, nil
}

// Capabilities returns which capabilities are already enabled, as Apple's
// enum values.
func (c *Client) Capabilities(bundleIDResource string) ([]string, error) {
	// No `limit` here: the API answers "The parameter 'limit' can not be
	// used with this request : This relationship does not support this
	// parameter." It is a relationship read, not a collection.
	result, err := c.Request("GET", "/bundleIds/"+bundleIDResource+"/bundleIdCapabilities", nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, item := range dataList(result) {
		if v, ok := attrString(item, "capabilityType"); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// EnableCapability turns a capability on. Already-enabled is not an error
// worth raising — the caller wants the end state, not the transition. A nil
// settings falls back to CapabilityDefaultSettings for capabilityType.
func (c *Client) EnableCapability(bundleIDResource, capabilityType string, settings []map[string]any) (map[string]any, error) {
	if settings == nil {
		settings = CapabilityDefaultSettings[capabilityType]
	}
	attrs := map[string]any{"capabilityType": capabilityType}
	if len(settings) > 0 {
		attrs["settings"] = settings
	}
	body := map[string]any{
		"data": map[string]any{
			"type":       "bundleIdCapabilities",
			"attributes": attrs,
			"relationships": map[string]any{
				"bundleId": map[string]any{"data": map[string]any{"type": "bundleIds", "id": bundleIDResource}},
			},
		},
	}
	result, err := c.Request("POST", "/bundleIdCapabilities", body)
	if err != nil {
		return nil, err
	}
	data, _ := result["data"].(map[string]any)
	return data, nil
}
