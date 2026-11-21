package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func mobileFixture(t *testing.T) (*StoreApplication, songya.ApplicationConfiguration) {
	t.Helper()
	dir := t.TempDir()
	config := songya.ApplicationConfiguration{Stores: map[string]string{"ari": filepath.Join(dir, "ari"), "other": filepath.Join(dir, "other")}, Password: "fixture-password", PublicURL: "https://songya.example", MobileState: filepath.Join(dir, "mobile.json"), AndroidAPK: filepath.Join(dir, "app.apk")}
	app := HandlerForStores(config)
	t.Cleanup(func() { _ = app.Close() })
	return app, config
}
func mobileRequest(app *StoreApplication, method, path string, input any, admin bool, cookie *http.Cookie) *httptest.ResponseRecorder {
	var body []byte
	var contentType string
	if form, ok := input.(url.Values); ok {
		body = []byte(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	} else if input != nil {
		body, _ = json.Marshal(input)
		contentType = "application/json"
	}
	r := httptest.NewRequest(method, "https://songya.example"+path, bytes.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if admin {
		r.SetBasicAuth("admin", "fixture-password")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}
func mobileJSON(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	var value map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

type mobilePhone struct {
	key    *ecdsa.PrivateKey
	public string
}

func newMobilePhone(t *testing.T) mobilePhone {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	return mobilePhone{key, base64.StdEncoding.EncodeToString(der)}
}
func pairMobileFixture(t *testing.T, app *StoreApplication, store string) string {
	t.Helper()
	p := mobileJSON(t, mobileRequest(app, "POST", "/api/mobile/pair", map[string]string{"store": store}, true, nil), 201)
	if !strings.HasPrefix(p["qr"].(string), "data:image/png;base64,") {
		t.Fatal("missing QR image")
	}
	uri, err := url.Parse(p["uri"].(string))
	if err != nil || uri.Query().Get("url") != "https://songya.example" {
		t.Fatal("invalid pairing link")
	}
	return uri.Query().Get("token")
}
func (p mobilePhone) challenge(app *StoreApplication, pairing string) *httptest.ResponseRecorder {
	return mobileRequest(app, "POST", "/api/mobile/challenge", map[string]string{"publicKey": p.public, "label": "Synthetic phone", "pairingToken": pairing}, false, nil)
}
func (p mobilePhone) proof(t *testing.T, challenge map[string]any) map[string]string {
	t.Helper()
	sum := sha256.Sum256([]byte(challenge["message"].(string)))
	signature, err := ecdsa.SignASN1(rand.Reader, p.key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"id": challenge["id"].(string), "signature": base64.StdEncoding.EncodeToString(signature)}
}
func (p mobilePhone) handoff(t *testing.T, app *StoreApplication, pairing string) string {
	t.Helper()
	c := mobileJSON(t, p.challenge(app, pairing), 200)
	response := mobileJSON(t, mobileRequest(app, "POST", "/api/mobile/authenticate", p.proof(t, c), false, nil), 200)
	return response["handoff"].(string)
}
func redeemMobileFixture(t *testing.T, app *StoreApplication, handoff string) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest("POST", "https://songya.example/api/mobile/session", strings.NewReader(url.Values{"handoff": {handoff}}.Encode()))
	r.Header.Set("Origin", "null")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("handoff: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("insecure device cookie")
	}
	return cookies[0]
}
func TestMobilePairingScopesEveryRouteAndSurvivesRestart(t *testing.T) {
	app, config := mobileFixture(t)
	phone := newMobilePhone(t)
	if phone.challenge(app, "").Code != 401 {
		t.Fatal("unpaired key accepted")
	}
	pairing := pairMobileFixture(t, app, "ari")
	handoff := phone.handoff(t, app, pairing)
	cookie := redeemMobileFixture(t, app, handoff)
	if phone.challenge(app, pairing).Code != 401 {
		t.Fatal("QR reused")
	}
	if mobileRequest(app, "POST", "/api/mobile/session", url.Values{"handoff": {handoff}}, false, nil).Code != 401 {
		t.Fatal("handoff reused")
	}
	for _, path := range []string{"/stores/ari/", "/stores/ari/api/records", "/assets/theme.js"} {
		if r := mobileRequest(app, "GET", path, nil, false, cookie); r.Code != 200 {
			t.Fatalf("%s: %d", path, r.Code)
		}
	}
	page := mobileRequest(app, "GET", "/stores/ari/", nil, false, cookie).Body.String()
	if strings.Contains(page, `<option value="other"`) {
		t.Fatal("another store leaked into phone navigation")
	}
	for _, path := range []string{"/stores/other/", "/stores/other/api/records", "/stores/other/api/chat/settings"} {
		if mobileRequest(app, "GET", path, nil, false, cookie).Code != 404 {
			t.Fatal("cross-store access", path)
		}
	}
	if mobileRequest(app, "POST", "/api/mobile/pair", map[string]string{"store": "other"}, false, cookie).Code != 403 {
		t.Fatal("cross-store pairing")
	}
	state, err := os.ReadFile(config.MobileState)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(state, []byte(pairing)) || bytes.Contains(state, []byte(handoff)) || bytes.Contains(state, []byte(cookie.Value)) {
		t.Fatal("plaintext token persisted")
	}
	if info, _ := os.Stat(config.MobileState); info.Mode().Perm() != 0600 {
		t.Fatal("authorization file permissions")
	}
	_ = app.Close()
	restarted := HandlerForStores(config)
	defer restarted.Close()
	renewed := redeemMobileFixture(t, restarted, phone.handoff(t, restarted, ""))
	if mobileRequest(restarted, "GET", "/stores/ari/api/records", nil, false, renewed).Code != 200 {
		t.Fatal("device did not survive restart")
	}
}
func TestMobileProofConsumptionExpiryRevocationAndLogout(t *testing.T) {
	app, _ := mobileFixture(t)
	phone := newMobilePhone(t)
	pairing := pairMobileFixture(t, app, "ari")
	challenge := mobileJSON(t, phone.challenge(app, pairing), 200)
	wrong := newMobilePhone(t).proof(t, challenge)
	if mobileRequest(app, "POST", "/api/mobile/authenticate", wrong, false, nil).Code != 401 {
		t.Fatal("invalid proof accepted")
	}
	if mobileRequest(app, "POST", "/api/mobile/authenticate", phone.proof(t, challenge), false, nil).Code != 401 {
		t.Fatal("proof attempt not consumed")
	}
	cookie := redeemMobileFixture(t, app, phone.handoff(t, app, pairing))
	stale := phone.handoff(t, app, "")
	challenge = mobileJSON(t, phone.challenge(app, ""), 200)
	status := mobileJSON(t, mobileRequest(app, "GET", "/api/mobile?store=ari", nil, true, nil), 200)
	id := status["devices"].([]any)[0].(map[string]any)["id"].(string)
	if mobileRequest(app, "DELETE", "/api/mobile/devices/"+id, nil, true, nil).Code != 200 {
		t.Fatal("revocation failed")
	}
	if mobileRequest(app, "GET", "/stores/ari/api/records", nil, false, cookie).Code != 401 {
		t.Fatal("session survived revocation")
	}
	if mobileRequest(app, "POST", "/api/mobile/authenticate", phone.proof(t, challenge), false, nil).Code != 401 {
		t.Fatal("proof survived revocation")
	}
	if mobileRequest(app, "POST", "/api/mobile/session", url.Values{"handoff": {stale}}, false, nil).Code != 401 {
		t.Fatal("handoff survived revocation")
	}
	cookie = redeemMobileFixture(t, app, phone.handoff(t, app, pairMobileFixture(t, app, "ari")))
	if mobileRequest(app, "POST", "/api/logout", nil, false, cookie).Code != 200 || phone.challenge(app, "").Code != 401 {
		t.Fatal("logout did not revoke device")
	}
	expiring := pairMobileFixture(t, app, "ari")
	now := time.Now()
	app.mobile.clock = func() time.Time { return now.Add(6 * time.Minute) }
	if phone.challenge(app, expiring).Code != 401 {
		t.Fatal("expired QR accepted")
	}
}
func TestMobileStorePairingAndDeviceManagementCannotCrossStores(t *testing.T) {
	app, _ := mobileFixture(t)
	first, second := newMobilePhone(t), newMobilePhone(t)
	own := redeemMobileFixture(t, app, first.handoff(t, app, pairMobileFixture(t, app, "ari")))
	_ = redeemMobileFixture(t, app, second.handoff(t, app, pairMobileFixture(t, app, "other")))
	other := mobileJSON(t, mobileRequest(app, "GET", "/api/mobile?store=other", nil, true, nil), 200)["devices"].([]any)[0].(map[string]any)["id"].(string)
	if mobileRequest(app, "DELETE", "/api/mobile/devices/"+other, nil, false, own).Code != 404 {
		t.Fatal("cross-store revocation")
	}
	ownDevices := mobileJSON(t, mobileRequest(app, "GET", "/api/mobile?store=other", nil, false, own), 200)["devices"].([]any)
	if len(ownDevices) != 1 || ownDevices[0].(map[string]any)["id"] == other {
		t.Fatal("device metadata leaked")
	}
	if first.challenge(app, pairMobileFixture(t, app, "other")).Code != 409 {
		t.Fatal("key silently changed stores")
	}
}
func TestMobilePasswordChangesRequirePairingAndDoNotReviveOldCookies(t *testing.T) {
	app, config := mobileFixture(t)
	phone := newMobilePhone(t)
	cookie := redeemMobileFixture(t, app, phone.handoff(t, app, pairMobileFixture(t, app, "ari")))
	_ = app.Close()
	config.Password = "new-password"
	changed := HandlerForStores(config)
	defer changed.Close()
	if phone.challenge(changed, "").Code != 401 || mobileRequest(changed, "GET", "/stores/ari/", nil, false, cookie).Code != 401 {
		t.Fatal("old authorization survived password change")
	}
	state, _ := loadMobileState(config.MobileState)
	token, _ := changed.mobile.issue(&state, mobileToken{Kind: "pair", Store: "ari"}, 300)
	if err := saveMobileState(config.MobileState, state); err != nil {
		t.Fatal(err)
	}
	_ = redeemMobileFixture(t, changed, phone.handoff(t, changed, token))
	if mobileRequest(changed, "GET", "/stores/ari/", nil, false, cookie).Code != 401 {
		t.Fatal("old cookie revived after fresh pairing")
	}
}
func TestMobileOriginChecksAndUnreadableAuthorizationsKeepMemoryAccessWorking(t *testing.T) {
	app, config := mobileFixture(t)
	r := httptest.NewRequest("POST", "https://songya.example/api/mobile/pair", strings.NewReader(`{"store":"ari"}`))
	r.SetBasicAuth("admin", "fixture-password")
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	if mobileRequest(app, "GET", "/api/mobile/challenge", nil, false, nil).Code != 405 {
		t.Fatal("public proof method not enforced")
	}
	damaged := []byte(`{"devices":broken`)
	if err := os.WriteFile(config.MobileState, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if mobileRequest(app, "GET", "/api/mobile?store=ari", nil, true, nil).Code != 503 {
		t.Fatal("damaged credentials hidden")
	}
	if mobileRequest(app, "GET", "/stores/ari/api/records", nil, true, nil).Code != 200 {
		t.Fatal("device failure blocked healthy memories")
	}
	after, _ := os.ReadFile(config.MobileState)
	if !bytes.Equal(after, damaged) {
		t.Fatal("damaged authorizations overwritten")
	}
}
func TestMobileRecoversInterruptedAuthorizationPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	state := emptyMobileState()
	if err := saveMobileState(path, state); err != nil {
		t.Fatal(err)
	}
	state.Revision = 2
	state.Devices["retained"] = mobileDevice{ID: "retained", Label: "Retained phone"}
	data, _ := json.Marshal(state)
	pending := path + ".complete.pending"
	if err := os.WriteFile(pending, data, 0600); err != nil {
		t.Fatal(err)
	}
	partial := path + ".partial.pending"
	_ = os.WriteFile(partial, []byte(`{"devices":`), 0600)
	recovered, err := loadMobileState(path)
	if err != nil || recovered.Devices["retained"].Label != "Retained phone" {
		t.Fatal("completed authorization was not recovered", err)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatal("unparsed bytes were discarded")
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("completed write not published")
	}
}

func TestBrowserMobileManagementKeepsTheSelectedBasicAuthDirectory(t *testing.T) {
	app, _ := mobileFixture(t)
	if mobileRequest(app, "GET", "/stores/ari/api/mobile?store=ari", nil, true, nil).Code != 200 {
		t.Fatal("scoped status failed")
	}
	response := mobileJSON(t, mobileRequest(app, "POST", "/stores/ari/api/mobile/pair", map[string]string{"store": "ari"}, true, nil), 201)
	uri, _ := url.Parse(response["uri"].(string))
	phone := newMobilePhone(t)
	cookie := redeemMobileFixture(t, app, phone.handoff(t, app, uri.Query().Get("token")))
	if mobileRequest(app, "GET", "/stores/other/api/mobile?store=other", nil, false, cookie).Code != 404 {
		t.Fatal("scoped mobile alias bypassed store isolation")
	}
	if mobileRequest(app, "POST", "/stores/ari/api/logout", nil, false, cookie).Code != 200 || phone.challenge(app, "").Code != 401 {
		t.Fatal("scoped logout did not revoke phone")
	}
}
