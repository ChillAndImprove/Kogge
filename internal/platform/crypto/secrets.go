// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package crypto provides secret encryption under the master key, random tokens and password hashing.
package crypto

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kogge.de/kogge/internal/platform/files"
)

// Keyring holds the master keys. The first key encrypts; all keys decrypt (for rotation).
type Keyring struct {
	current string
	ids     []string // in file order
	keys    map[string]cipher.AEAD
	macs    map[string][]byte // keys of keyed hashes, derived from each master key
}

// macInfo derives the keys of keyed hashes from the master keys (HKDF), apart from the keys that encrypt.
const macInfo = "kogge keyed hash v1"

// GenerateKey returns a new random 256-bit key in the master key file format.
func GenerateKey() string {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// ParseKeyring parses a master key file: one base64-encoded 256-bit key per line, the current key first.
// Empty lines and lines starting with # are ignored.
func ParseKeyring(content string) (*Keyring, error) {
	kr := &Keyring{keys: map[string]cipher.AEAD{}, macs: map[string][]byte{}}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(line)
		if err != nil || len(raw) != 32 {
			return nil, errors.New("master key: every key must be 32 bytes, base64-encoded")
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		mac, err := hkdf.Key(sha256.New, raw, nil, macInfo, 32)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		id := hex.EncodeToString(sum[:4])
		if kr.current == "" {
			kr.current = id
		}
		if _, dup := kr.keys[id]; !dup {
			kr.ids = append(kr.ids, id)
		}
		kr.keys[id], kr.macs[id] = aead, mac
	}
	if kr.current == "" {
		return nil, errors.New("master key: no key found")
	}
	return kr, nil
}

// LoadOrCreateKeyring reads the master key file. If path is empty, the key is kept in the data directory and
// created on first use; production installations should keep it outside the data volume.
func LoadOrCreateKeyring(path, dataDir string) (kr *Keyring, created bool, err error) {
	if path == "" {
		path = filepath.Join(dataDir, "master.key")
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			if err := os.MkdirAll(dataDir, 0o750); err != nil {
				return nil, false, err
			}
			content := "# Kogge master key. Keep a copy in a safe place: secrets cannot be decrypted without it.\n" +
				GenerateKey() + "\n"
			// Nodes that start for the first time together on one data volume agree on one key.
			if _, created, err = files.WriteOnce(path, []byte(content), 0o600); err != nil {
				return nil, false, err
			}
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("master key: %w", err)
	}
	kr, err = ParseKeyring(string(b))
	return kr, created, err
}

// KeyPath returns the master key file: path, or the default in the data directory.
func KeyPath(path, dataDir string) string {
	if path == "" {
		return filepath.Join(dataDir, "master.key")
	}
	return path
}

// LoadKeyring reads the master key file without creating it (see KeyPath).
func LoadKeyring(path, dataDir string) (*Keyring, error) {
	b, err := os.ReadFile(KeyPath(path, dataDir))
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	return ParseKeyring(string(b))
}

// CurrentKeyID returns the id of the encrypting key.
func (k *Keyring) CurrentKeyID() string { return k.current }

// KeyIDs returns the ids of all keys, the current key first. An id is derived from the key and reveals nothing
// about it.
func (k *Keyring) KeyIDs() []string { return append([]string(nil), k.ids...) }

// Seal encrypts plaintext. aad binds the ciphertext to its owner (e.g. "smtp:password"), so it cannot be moved
// to another record. The result has the form v1:<key id>:<base64(nonce||ciphertext)>.
func (k *Keyring) Seal(plaintext []byte, aad string) (string, error) {
	aead := k.keys[k.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return "v1:" + k.current + ":" + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal with the same aad.
func (k *Keyring) Open(sealed, aad string) ([]byte, error) {
	parts := strings.SplitN(sealed, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return nil, errors.New("secret: unknown format")
	}
	aead, ok := k.keys[parts[1]]
	if !ok {
		return nil, fmt.Errorf("secret: key %s is not in the master key file", parts[1])
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < aead.NonceSize() {
		return nil, errors.New("secret: corrupt value")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(aad))
	if err != nil {
		return nil, errors.New("secret: cannot decrypt (wrong key or record)")
	}
	return plain, nil
}

// MAC returns the keyed hash (HMAC-SHA-256) of data under the current key, for values that are stored to be
// recognised and must not be guessable from a copy of the database alone, as values of few bits are from their
// plain hashes. purpose separates the uses: the same data has other hashes for other purposes.
func (k *Keyring) MAC(purpose string, data []byte) []byte { return k.mac(k.current, purpose, data) }

// MACs returns the keyed hashes of data under every key, the current key first: values hashed before another key
// became the current one are recognised as long as their key is in the master key file (as sealed values can be
// read).
func (k *Keyring) MACs(purpose string, data []byte) [][]byte {
	out := make([][]byte, len(k.ids))
	for i, id := range k.ids {
		out[i] = k.mac(id, purpose, data)
	}
	return out
}

func (k *Keyring) mac(id, purpose string, data []byte) []byte {
	h := hmac.New(sha256.New, k.macs[id])
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write(data)
	return h.Sum(nil)
}

// KeyID returns the key id of a sealed value.
func KeyID(sealed string) string {
	parts := strings.SplitN(sealed, ":", 3)
	if len(parts) != 3 {
		return ""
	}
	return parts[1]
}
