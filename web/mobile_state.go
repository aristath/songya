package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type mobileDevice struct {
	ID         string `json:"id"`
	PublicKey  string `json:"publicKey"`
	Store      string `json:"store"`
	Label      string `json:"label"`
	Auth       string `json:"auth"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
}
type mobileToken struct {
	Kind        string `json:"kind"`
	Store       string `json:"store"`
	Device      string `json:"device,omitempty"`
	PublicKey   string `json:"publicKey,omitempty"`
	PairingHash string `json:"pairingHash,omitempty"`
	Message     string `json:"message,omitempty"`
	Label       string `json:"label,omitempty"`
	Auth        string `json:"auth"`
	ExpiresAt   int64  `json:"expiresAt"`
}
type mobileState struct {
	Revision int64                   `json:"revision"`
	Devices  map[string]mobileDevice `json:"devices"`
	Tokens   map[string]mobileToken  `json:"tokens"`
	Sessions map[string]string       `json:"sessions"`
}

func mobileDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func emptyMobileState() mobileState {
	return mobileState{Devices: map[string]mobileDevice{}, Tokens: map[string]mobileToken{}, Sessions: map[string]string{}}
}
func readMobileState(path string) (mobileState, error) {
	state := emptyMobileState()
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Devices == nil || state.Tokens == nil || state.Sessions == nil {
		return state, errors.New("device authorization file is incomplete; its bytes are preserved")
	}
	return state, nil
}
func syncMobileDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func loadMobileState(path string) (mobileState, error) {
	state, original := readMobileState(path)
	if errors.Is(original, fs.ErrNotExist) {
		original = nil
	}
	// A fully synced write whose publication was interrupted is still useful work.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return state, err
	}
	candidate := ""
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), filepath.Base(path)+".") || !strings.HasSuffix(entry.Name(), ".pending") {
			continue
		}
		name := filepath.Join(filepath.Dir(path), entry.Name())
		pending, err := readMobileState(name)
		if err == nil && ((original != nil && candidate == "") || pending.Revision > state.Revision) {
			state = pending
			candidate = name
		}
	}
	if candidate != "" {
		file, err := os.Open(candidate)
		if err != nil {
			return state, err
		}
		if err = errors.Join(file.Sync(), file.Close()); err != nil {
			return state, err
		}
		if err = os.Rename(candidate, path); err != nil {
			return state, err
		}
		return state, syncMobileDirectory(path)
	}
	return state, original
}
func saveMobileState(path string, state mobileState) error {
	state.Revision++
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	pending := path + "." + mobileDigest(string(data)) + ".pending"
	// Failed publication retains these bytes for the next load to recover.
	file, err := os.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, written := file.Write(data)
	if err = errors.Join(written, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err = os.Rename(pending, path); err != nil {
		return err
	}
	if err = syncMobileDirectory(path); err != nil {
		return fmt.Errorf("sync device authorizations: %w", err)
	}
	return nil
}
