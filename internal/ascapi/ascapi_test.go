package ascapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDerToRawSignatureShortForm(t *testing.T) {
	// SEQUENCE { INTEGER 0x01 0x02, INTEGER 0x03 0x04 }, short-form length.
	der := []byte{0x30, 0x08, 0x02, 0x02, 0x01, 0x02, 0x02, 0x02, 0x03, 0x04}
	got, err := derToRawSignature(der, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x00, 0x01, 0x02, 0x00, 0x00, 0x03, 0x04}
	if !bytes.Equal(got, want) {
		t.Errorf("derToRawSignature = %x, want %x", got, want)
	}
}

func TestDerToRawSignatureStripsLeadingZeroByte(t *testing.T) {
	// A high-bit-set integer carries a leading 0x00 in DER that the raw form
	// must not keep.
	der := []byte{0x30, 0x06, 0x02, 0x02, 0x00, 0xFF, 0x02, 0x00}
	got, err := derToRawSignature(der, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0xFF, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("derToRawSignature = %x, want %x", got, want)
	}
}

func TestDerToRawSignatureRejectsNonSequence(t *testing.T) {
	if _, err := derToRawSignature([]byte{0x02, 0x01, 0x01}, 32); err == nil {
		t.Error("expected an error for a non-SEQUENCE input")
	}
}

func TestDerToRawSignatureRejectsEmpty(t *testing.T) {
	if _, err := derToRawSignature(nil, 32); err == nil {
		t.Error("expected an error for empty input")
	}
}

func TestLoadCredentialsRequiresAllThree(t *testing.T) {
	t.Setenv("APP_STORE_CONNECT_KEY_PATH", "")
	t.Setenv("APP_STORE_CONNECT_KEY_ID", "K1")
	t.Setenv("APP_STORE_CONNECT_ISSUER_ID", "ISSUER")
	if LoadCredentials() != nil {
		t.Error("LoadCredentials should be nil when the key path is unset")
	}
}

func TestMissingCredentialReasonNamesUnsetVars(t *testing.T) {
	t.Setenv("APP_STORE_CONNECT_KEY_PATH", "")
	t.Setenv("APP_STORE_CONNECT_KEY_ID", "")
	t.Setenv("APP_STORE_CONNECT_ISSUER_ID", "ISSUER")
	reason := MissingCredentialReason()
	if !strings.Contains(reason, "APP_STORE_CONNECT_KEY_PATH") || !strings.Contains(reason, "APP_STORE_CONNECT_KEY_ID") {
		t.Errorf("MissingCredentialReason = %q, want it to name both unset vars", reason)
	}
	if strings.Contains(reason, "APP_STORE_CONNECT_ISSUER_ID") {
		t.Errorf("MissingCredentialReason = %q, should not name the var that IS set", reason)
	}
}

func TestMissingCredentialReasonNamesBadPath(t *testing.T) {
	t.Setenv("APP_STORE_CONNECT_KEY_PATH", "/nonexistent/AuthKey_X.p8")
	t.Setenv("APP_STORE_CONNECT_KEY_ID", "K1")
	t.Setenv("APP_STORE_CONNECT_ISSUER_ID", "ISSUER")
	reason := MissingCredentialReason()
	if !strings.Contains(reason, "points at a file that is not there") {
		t.Errorf("MissingCredentialReason = %q, want the file-not-found message", reason)
	}
}

func TestRequestSurfacesMissingCredentials(t *testing.T) {
	c := &Client{Creds: nil, HTTPClient: http.DefaultClient, Now: time.Now}
	_, err := c.Request("GET", "/bundleIds", nil)
	if err == nil || !strings.Contains(err.Error(), "No App Store Connect API key configured") {
		t.Errorf("Request with nil creds = %v, want a missing-credential error", err)
	}
}

// --- full round-trip tests, real ES256 signing, fake transport -------------

// roundTripFunc adapts a function to http.RoundTripper — the Go analogue of
// the Python tests' `opener` fake, which fed `admin_lib.appstoreconnect`
// canned JSON without a real network call.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, payload any) *http.Response {
	b, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     http.Header{},
	}
}

// testKeyPath generates a throwaway EC P-256 private key with openssl (the
// same tool Token() signs with) and returns its path. Skips the test when
// openssl isn't on PATH, matching how the package itself degrades.
func testKeyPath(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not on PATH")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "AuthKey_TEST123.p8")
	if out, err := exec.Command("openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", path).CombinedOutput(); err != nil {
		t.Fatalf("openssl keygen failed: %v: %s", err, out)
	}
	return path
}

func TestTokenProducesAThreePartJWT(t *testing.T) {
	creds := &Credentials{KeyPath: testKeyPath(t), KeyID: "K1", IssuerID: "ISSUER"}
	tok, err := Token(creds, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("Token produced %d parts, want 3 (header.payload.signature)", len(parts))
	}
	for i, p := range parts {
		if p == "" {
			t.Errorf("Token part %d is empty", i)
		}
	}
}

func TestFindBundleIDMatchesOnIdentifier(t *testing.T) {
	var gotURL string
	c := &Client{
		Creds: &Credentials{KeyPath: testKeyPath(t), KeyID: "K1", IssuerID: "ISSUER"},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			return jsonResponse(200, map[string]any{
				"data": []any{
					map[string]any{"id": "BID1", "attributes": map[string]any{"identifier": "com.example.app"}},
				},
			}), nil
		})},
		Now: time.Now,
	}
	resource, err := c.FindBundleID("com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if resource == nil || resource["id"] != "BID1" {
		t.Errorf("FindBundleID = %v, want id BID1", resource)
	}
	if !strings.Contains(gotURL, "filter[identifier]=com.example.app") {
		t.Errorf("request URL = %q, want the identifier filter", gotURL)
	}
}

func TestFindBundleIDReturnsNilWhenUnregistered(t *testing.T) {
	c := &Client{
		Creds:      &Credentials{KeyPath: testKeyPath(t), KeyID: "K1", IssuerID: "ISSUER"},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(200, map[string]any{"data": []any{}}), nil })},
		Now:        time.Now,
	}
	resource, err := c.FindBundleID("com.example.missing")
	if err != nil {
		t.Fatal(err)
	}
	if resource != nil {
		t.Errorf("FindBundleID = %v, want nil for an unregistered identifier", resource)
	}
}

func TestEnableCapabilityDefaultsICloudSettings(t *testing.T) {
	var body map[string]any
	c := &Client{
		Creds: &Credentials{KeyPath: testKeyPath(t), KeyID: "K1", IssuerID: "ISSUER"},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
			return jsonResponse(201, map[string]any{"data": map[string]any{"id": "CAP1"}}), nil
		})},
		Now: time.Now,
	}
	if _, err := c.EnableCapability("BID1", "ICLOUD", nil); err != nil {
		t.Fatal(err)
	}
	data := body["data"].(map[string]any)
	attrs := data["attributes"].(map[string]any)
	if attrs["capabilityType"] != "ICLOUD" {
		t.Errorf("capabilityType = %v, want ICLOUD", attrs["capabilityType"])
	}
	if _, ok := attrs["settings"]; !ok {
		t.Error("EnableCapability(ICLOUD) sent no settings — ICLOUD requires ICLOUD_VERSION or the API 409s")
	}
}

func TestEnableCapabilityOmitsSettingsWhenNoDefault(t *testing.T) {
	var body map[string]any
	c := &Client{
		Creds: &Credentials{KeyPath: testKeyPath(t), KeyID: "K1", IssuerID: "ISSUER"},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
			return jsonResponse(201, map[string]any{"data": map[string]any{"id": "CAP2"}}), nil
		})},
		Now: time.Now,
	}
	if _, err := c.EnableCapability("BID1", "KEYCHAIN_SHARING", nil); err != nil {
		t.Fatal(err)
	}
	attrs := body["data"].(map[string]any)["attributes"].(map[string]any)
	if _, ok := attrs["settings"]; ok {
		t.Errorf("EnableCapability(KEYCHAIN_SHARING) sent settings %v, want none", attrs["settings"])
	}
}

func TestRequestSurfacesAPIErrorDetail(t *testing.T) {
	c := &Client{
		Creds: &Credentials{KeyPath: testKeyPath(t), KeyID: "K1", IssuerID: "ISSUER"},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(409, map[string]any{
				"errors": []any{map[string]any{"title": "Conflict", "detail": "already exists"}},
			}), nil
		})},
		Now: time.Now,
	}
	_, err := c.Request("POST", "/bundleIds", map[string]any{"data": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "Conflict: already exists") {
		t.Errorf("Request error = %v, want it to carry Apple's error detail", err)
	}
}
