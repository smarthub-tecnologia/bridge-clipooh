package cipher

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// keyVersions controls which key versions are loaded at startup.
// Append "V2", "V3", etc. here when rotating keys.
var keyVersions = []string{"V1"}

var keyMap map[string][]byte

func init() {
	if testing.Testing() {
		// In test binaries, TestMain injects keys directly — skip env loading.
		keyMap = make(map[string][]byte)
		return
	}
	keyMap = mustLoadKeys()
}

func mustLoadKeys() map[string][]byte {
	m := make(map[string][]byte)
	for _, ver := range keyVersions {
		envName := "META_TOKEN_ENCRYPTION_KEY_" + ver
		hexVal := os.Getenv(envName)
		if hexVal == "" {
			panic(fmt.Sprintf("cipher: missing required env %s", envName))
		}
		key, err := hex.DecodeString(hexVal)
		if err != nil || len(key) != 32 {
			panic(fmt.Sprintf("cipher: invalid key in %s: must be 64 hex chars (32 bytes)", envName))
		}
		m[strings.ToLower(ver)] = key
	}
	return m
}

// DecryptToken decrypts a Meta Cloud API token encrypted by Next.js with AES-256-GCM.
//
// Supports two ciphertext formats (auto-detected):
//
//  1. Next.js format (current): "ivHex:tagHex:ciphertextHex"
//     Three colon-separated hex strings produced by lib/crypto.ts encryptToken().
//
//  2. Legacy Bridge format: base64(nonce[12] || tag[16] || ciphertext)
//     Concatenated bytes encoded as base64 Standard.
func DecryptToken(ciphertext string, keyVersion string) (string, error) {
	key, ok := keyMap[strings.ToLower(keyVersion)]
	if !ok {
		return "", fmt.Errorf("unknown key version: %s", keyVersion)
	}

	var nonce, authTag, ct []byte

	if strings.Contains(ciphertext, ":") {
		// Next.js format: ivHex:tagHex:ciphertextHex
		parts := strings.SplitN(ciphertext, ":", 3)
		if len(parts) != 3 {
			return "", errors.New("decryption failed: invalid colon-separated format")
		}
		var err error
		nonce, err = hex.DecodeString(parts[0])
		if err != nil {
			return "", errors.New("decryption failed: invalid nonce hex")
		}
		authTag, err = hex.DecodeString(parts[1])
		if err != nil {
			return "", errors.New("decryption failed: invalid tag hex")
		}
		ct, err = hex.DecodeString(parts[2])
		if err != nil {
			return "", errors.New("decryption failed: invalid ciphertext hex")
		}
	} else {
		// Legacy Bridge format: base64(nonce[12] || tag[16] || ciphertext)
		raw, err := base64.StdEncoding.DecodeString(ciphertext)
		if err != nil || len(raw) < 28 {
			return "", errors.New("decryption failed: invalid base64 or too short")
		}
		nonce = raw[0:12]
		authTag = raw[12:28]
		ct = raw[28:]
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", errors.New("decryption failed")
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.New("decryption failed")
	}

	// gcm.Open expects ciphertext || authTag concatenated.
	combined := append(ct, authTag...)
	plaintext, err := gcm.Open(nil, nonce, combined, nil)
	if err != nil {
		return "", errors.New("decryption failed")
	}

	return string(plaintext), nil
}
