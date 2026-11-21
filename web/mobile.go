package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aristath/songya"
	qrcode "github.com/skip2/go-qrcode"
)

const mobileCookie = "songya_session"

type mobileAccess struct {
	Store  string
	Device string
}
type mobileServer struct {
	config songya.ApplicationConfiguration
	mu     sync.Mutex
	clock  func() time.Time
}

func newMobileServer(config songya.ApplicationConfiguration) *mobileServer {
	return &mobileServer{config: config, clock: time.Now}
}
func opaqueMobile() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}
func (m *mobileServer) storeAuth(store string) string {
	return mobileDigest(m.config.Password + "\n" + store + "\n" + m.config.Stores[store])
}
func (m *mobileServer) valid(device mobileDevice) bool {
	return device.ID != "" && m.config.Stores[device.Store] != "" && device.Auth == m.storeAuth(device.Store)
}
func (m *mobileServer) issue(s *mobileState, token mobileToken, seconds int) (string, int64) {
	now := m.clock().UnixMilli()
	for key, value := range s.Tokens {
		if value.ExpiresAt <= now {
			delete(s.Tokens, key)
		}
	}
	raw := opaqueMobile()
	token.ExpiresAt = now + int64(seconds)*1000
	token.Auth = m.storeAuth(token.Store)
	s.Tokens[mobileDigest(raw)] = token
	return raw, token.ExpiresAt
}
func (m *mobileServer) token(s *mobileState, hashed, kind string) (mobileToken, error) {
	t, ok := s.Tokens[hashed]
	if !ok || t.Kind != kind || t.ExpiresAt <= m.clock().UnixMilli() || m.config.Stores[t.Store] == "" || t.Auth != m.storeAuth(t.Store) {
		return t, errors.New("Pairing or authentication expired. Reconnect the device.")
	}
	return t, nil
}
func mobilePublic(raw string) (*ecdsa.PublicKey, string, error) {
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, "", err
	}
	value, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", err
	}
	key, ok := value.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, "", errors.New("Use a P-256 public key")
	}
	canonical, err := x509.MarshalPKIXPublicKey(key)
	return key, base64.StdEncoding.EncodeToString(canonical), err
}
func (m *mobileServer) authenticate(r *http.Request) (mobileAccess, error) {
	if m.config.MobileState == "" || m.config.PublicURL == "" {
		return mobileAccess{}, errors.New("Device pairing is not configured")
	}
	cookie, err := r.Cookie(mobileCookie)
	if err != nil {
		return mobileAccess{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := loadMobileState(m.config.MobileState)
	if err != nil {
		return mobileAccess{}, err
	}
	device := s.Devices[s.Sessions[mobileDigest(cookie.Value)]]
	if !m.valid(device) {
		return mobileAccess{}, errors.New("This phone must pair again")
	}
	return mobileAccess{Store: device.Store, Device: device.ID}, nil
}
func (m *mobileServer) public(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path != "/api/mobile/challenge" && path != "/api/mobile/authenticate" && path != "/api/mobile/session" {
		return false
	}
	m.serve(w, r, mobileAccess{}, false)
	return true
}
func (m *mobileServer) serve(w http.ResponseWriter, r *http.Request, access mobileAccess, admin bool) {
	w.Header().Set("Cache-Control", "no-store")
	if m.config.MobileState == "" || m.config.PublicURL == "" {
		writeStatus(w, 503, map[string]string{"error": "Configure Songya's public HTTPS address before pairing"})
		return
	}
	origin := r.Header.Get("Origin")
	native := r.URL.Path == "/api/mobile/session" && origin == "null"
	if origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host && !native {
		writeStatus(w, 403, map[string]string{"error": "Open Songya directly to manage devices"})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/mobile" {
		m.mu.Lock()
		defer m.mu.Unlock()
		s, err := loadMobileState(m.config.MobileState)
		if err != nil {
			writeStatus(w, 503, map[string]string{"error": "Device authorizations need repair; existing data is preserved"})
			return
		}
		store := r.URL.Query().Get("store")
		if !admin {
			store = access.Store
		}
		devices := []map[string]any{}
		for _, d := range s.Devices {
			if d.Store == store && m.valid(d) {
				devices = append(devices, map[string]any{"id": d.ID, "label": d.Label, "createdAt": d.CreatedAt, "lastUsedAt": d.LastUsedAt})
			}
		}
		_, err = os.Stat(m.config.AndroidAPK)
		write(w, map[string]any{"public_url": m.config.PublicURL, "devices": devices, "apk_available": err == nil, "paired": access.Device != ""})
		return
	}
	if r.Method != "POST" && !(r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/mobile/devices/")) {
		writeStatus(w, 405, map[string]string{"error": "Use POST or DELETE"})
		return
	}
	var input struct {
		Store        string `json:"store"`
		PublicKey    string `json:"publicKey"`
		PairingToken string `json:"pairingToken"`
		Label        string `json:"label"`
		ID           string `json:"id"`
		Signature    string `json:"signature"`
	}
	if r.URL.Path != "/api/mobile/session" && r.URL.Path != "/api/logout" && r.Method == "POST" {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeStatus(w, 415, map[string]string{"error": "Use JSON"})
			return
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
			writeStatus(w, 400, map[string]string{"error": "Invalid device request"})
			return
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := loadMobileState(m.config.MobileState)
	if err != nil {
		writeStatus(w, 503, map[string]string{"error": "Device authorizations need repair; existing data is preserved"})
		return
	}
	status := 200
	result := map[string]any{}
	persist := true
	var cookie string
	redirect := ""
	clearCookie := false
	unauthorized := func(message string) { status = 401; result = map[string]any{"error": message}; persist = false }
	switch r.URL.Path {
	case "/api/mobile/pair":
		if !admin && input.Store != access.Store {
			writeStatus(w, 403, map[string]string{"error": "Choose your own store"})
			return
		}
		if m.config.Stores[input.Store] == "" {
			writeStatus(w, 404, map[string]string{"error": "Unknown store"})
			return
		}
		raw, expires := m.issue(&state, mobileToken{Kind: "pair", Store: input.Store}, 300)
		uri := "songya://pair?" + url.Values{"url": {m.config.PublicURL}, "token": {raw}, "exp": {fmt.Sprint(expires)}}.Encode()
		png, e := qrcode.Encode(uri, qrcode.Medium, 512)
		if e != nil {
			writeStatus(w, 500, map[string]string{"error": "Could not create the pairing code"})
			return
		}
		result = map[string]any{"uri": uri, "expiresAt": expires, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}
		status = 201
	case "/api/mobile/challenge":
		_, key, e := mobilePublic(input.PublicKey)
		if e != nil {
			writeStatus(w, 400, map[string]string{"error": "Use a valid P-256 public key"})
			return
		}
		id := mobileDigest(key)
		previous := state.Devices[id]
		store := ""
		pairingHash := ""
		if input.PairingToken != "" {
			pairingHash = mobileDigest(input.PairingToken)
			pair, e := m.token(&state, pairingHash, "pair")
			if e != nil {
				unauthorized(e.Error())
				break
			}
			store = pair.Store
			if m.valid(previous) && previous.Store != store {
				writeStatus(w, 409, map[string]string{"error": "Unpair this phone before choosing another store"})
				return
			}
		} else if m.valid(previous) {
			store = previous.Store
		} else {
			unauthorized("This device must pair again")
			break
		}
		label := strings.TrimSpace(input.Label)
		if label == "" {
			label = "Android"
		}
		if runes := []rune(label); len(runes) > 100 {
			label = string(runes[:100])
		}
		message := "songya-web-auth-v1\n" + opaqueMobile()
		raw, expires := m.issue(&state, mobileToken{Kind: "challenge", Store: store, Device: id, PublicKey: key, Label: label, Message: message, PairingHash: pairingHash}, 60)
		result = map[string]any{"id": raw, "message": message, "expiresAt": expires}
	case "/api/mobile/authenticate":
		challenge, e := m.token(&state, mobileDigest(input.ID), "challenge")
		if e != nil {
			unauthorized(e.Error())
			break
		}
		delete(state.Tokens, mobileDigest(input.ID))
		key, _, e := mobilePublic(challenge.PublicKey)
		sig, se := base64.StdEncoding.DecodeString(input.Signature)
		sum := sha256.Sum256([]byte(challenge.Message))
		if e != nil || se != nil || !ecdsa.VerifyASN1(key, sum[:], sig) {
			status = 401
			result = map[string]any{"error": "Device authentication failed"}
			break
		}
		if challenge.PairingHash != "" {
			if _, e = m.token(&state, challenge.PairingHash, "pair"); e != nil {
				status = 401
				result = map[string]any{"error": e.Error()}
				break
			}
			delete(state.Tokens, challenge.PairingHash)
			revokeMobileDevice(&state, challenge.Device)
			state.Devices[challenge.Device] = mobileDevice{ID: challenge.Device, Store: challenge.Store, PublicKey: challenge.PublicKey, Label: challenge.Label, Auth: challenge.Auth, CreatedAt: m.clock().UnixMilli()}
		} else if !m.valid(state.Devices[challenge.Device]) {
			status = 401
			result = map[string]any{"error": "This device must pair again"}
			break
		}
		d := state.Devices[challenge.Device]
		d.LastUsedAt = m.clock().UnixMilli()
		state.Devices[d.ID] = d
		raw, expires := m.issue(&state, mobileToken{Kind: "handoff", Store: d.Store, Device: d.ID}, 60)
		result = map[string]any{"handoff": raw, "expiresAt": expires}
	case "/api/mobile/session":
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			writeStatus(w, 415, map[string]string{"error": "Use a form handoff"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if e := r.ParseForm(); e != nil || len(r.PostForm) != 1 || len(r.PostForm["handoff"]) != 1 {
			writeStatus(w, 400, map[string]string{"error": "Supply one handoff"})
			return
		}
		hashed := mobileDigest(r.PostForm.Get("handoff"))
		handoff, e := m.token(&state, hashed, "handoff")
		if e != nil {
			unauthorized(e.Error())
			break
		}
		if !m.valid(state.Devices[handoff.Device]) {
			unauthorized("This device must pair again")
			break
		}
		delete(state.Tokens, hashed)
		cookie = opaqueMobile()
		state.Sessions[mobileDigest(cookie)] = handoff.Device
		redirect = "/stores/" + url.PathEscape(handoff.Store) + "/"
	case "/api/logout":
		if access.Device == "" {
			unauthorized("No paired device session")
			break
		}
		revokeMobileDevice(&state, access.Device)
		result = map[string]any{"ok": true}
		clearCookie = true
	default:
		id, ok := strings.CutPrefix(r.URL.Path, "/api/mobile/devices/")
		device, exists := state.Devices[id]
		if !ok || r.Method != "DELETE" || !exists || (!admin && device.Store != access.Store) {
			writeStatus(w, 404, map[string]string{"error": "Unknown device"})
			return
		}
		revokeMobileDevice(&state, id)
		result = map[string]any{"revoked": true}
	}
	if persist {
		if e := saveMobileState(m.config.MobileState, state); e != nil {
			writeStatus(w, 503, map[string]string{"error": "Device authorization could not be saved; retry. Existing data is preserved."})
			return
		}
	}
	if cookie != "" {
		http.SetCookie(w, &http.Cookie{Name: mobileCookie, Value: cookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 10 * 365 * 86400})
	}
	if clearCookie {
		http.SetCookie(w, &http.Cookie{Name: mobileCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, MaxAge: -1})
	}
	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}
	writeStatus(w, status, result)
}
func revokeMobileDevice(state *mobileState, id string) {
	delete(state.Devices, id)
	for hash, device := range state.Sessions {
		if device == id {
			delete(state.Sessions, hash)
		}
	}
	for hash, token := range state.Tokens {
		if token.Device == id {
			delete(state.Tokens, hash)
		}
	}
}
