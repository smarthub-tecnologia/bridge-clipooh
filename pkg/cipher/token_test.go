package cipher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	testKey := make([]byte, 32)
	if _, err := rand.Read(testKey); err != nil {
		panic("cipher test: failed to generate test key: " + err.Error())
	}
	keyMap["v1"] = testKey
	os.Exit(m.Run())
}

// encrypt mirrors the Next.js AES-256-GCM encryption layout:
//
//	[0:12]  nonce
//	[12:28] authTag
//	[28:]   ciphertext
func encrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	ct := sealed[:len(sealed)-16]
	authTag := sealed[len(sealed)-16:]

	raw := append(append(nonce, authTag...), ct...)
	return base64.StdEncoding.EncodeToString(raw), nil
}

func TestDecryptToken_Success(t *testing.T) {
	want := "EAABsbCS1iHgBOZBF4fake_meta_token_value"
	ciphertext, err := encrypt(keyMap["v1"], want)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := DecryptToken(ciphertext, "v1")
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecryptToken_UnknownVersion(t *testing.T) {
	ciphertext, _ := encrypt(keyMap["v1"], "token")
	_, err := DecryptToken(ciphertext, "v99")
	if err == nil {
		t.Fatal("expected error for unknown key version, got nil")
	}
}

func TestDecryptToken_TamperedAuthTag(t *testing.T) {
	ciphertext, err := encrypt(keyMap["v1"], "token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	raw, _ := base64.StdEncoding.DecodeString(ciphertext)
	// Flip a byte in the auth tag region [12:28]
	raw[15] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)

	_, err = DecryptToken(tampered, "v1")
	if err == nil {
		t.Fatal("expected error for tampered auth tag, got nil")
	}
}

func TestDecryptToken_TruncatedCiphertext(t *testing.T) {
	// Fewer than 28 bytes after base64 decode → should fail immediately.
	short := base64.StdEncoding.EncodeToString([]byte("tooshort"))
	_, err := DecryptToken(short, "v1")
	if err == nil {
		t.Fatal("expected error for truncated ciphertext, got nil")
	}
}
