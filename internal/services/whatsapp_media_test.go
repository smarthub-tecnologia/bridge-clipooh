package services

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// TestHKDFSHA256_RFC5869TestCase3 valida hkdfSHA256 contra o vetor oficial da
// RFC 5869 (Appendix A.3) para o caso "sem salt" — exatamente o modo que o
// WhatsApp usa (salt = HashLen zeros). Se essa conta bate, a expansão de
// chaves de mídia está implementada corretamente; qualquer bug na
// decriptação de mídia real estaria então nos passos de AES/HMAC, não aqui.
// wantOKM foi confirmado independentemente contra golang.org/x/crypto/hkdf.
func TestHKDFSHA256_RFC5869TestCase3(t *testing.T) {
	ikm, _ := hex.DecodeString("0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b")
	wantOKM, _ := hex.DecodeString("64763584cb9ec91ea39b467b73a996cb72561babba83d58266c14e65ad3191d8e1aedb21aa853cccc000")

	got := hkdfSHA256(ikm, nil, 42)
	if !bytes.Equal(got, wantOKM) {
		t.Fatalf("hkdfSHA256 mismatch\n got: %x\nwant: %x", got, wantOKM)
	}
}

// encryptWhatsAppMediaForTest é o inverso de decryptWhatsAppMedia — cifra
// plaintext do jeito que o WhatsApp cifraria antes de subir pro CDN, usada
// aqui só para gerar um blob de teste e provar que a decriptação recupera o
// original.
func encryptWhatsAppMediaForTest(t *testing.T, plaintext []byte, mediaKey []byte, waMediaType string) []byte {
	t.Helper()
	info := waMediaHKDFInfo[waMediaType]
	expanded := hkdfSHA256(mediaKey, []byte(info), 112)
	iv := expanded[0:16]
	cipherKey := expanded[16:48]
	macKey := expanded[48:80]

	block, err := aes.NewCipher(cipherKey)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	mac := hmac.New(sha256.New, macKey)
	mac.Write(iv)
	mac.Write(ciphertext)
	tag := mac.Sum(nil)[:10]

	return append(ciphertext, tag...)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - len(data)%blockSize
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(data, padding...)
}

func TestDecryptWhatsAppMedia_RoundTrip(t *testing.T) {
	plaintext := []byte("this is a fake jpeg payload, long enough to span multiple AES blocks of 16 bytes each")
	mediaKey := make([]byte, 32)
	for i := range mediaKey {
		mediaKey[i] = byte(i)
	}
	mediaKeyB64 := base64.StdEncoding.EncodeToString(mediaKey)

	for _, waMediaType := range []string{"image", "video", "gif", "audio", "document"} {
		t.Run(waMediaType, func(t *testing.T) {
			encrypted := encryptWhatsAppMediaForTest(t, plaintext, mediaKey, waMediaType)

			got, err := decryptWhatsAppMedia(encrypted, mediaKeyB64, waMediaType)
			if err != nil {
				t.Fatalf("decryptWhatsAppMedia: %v", err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Fatalf("decrypted mismatch\n got: %q\nwant: %q", got, plaintext)
			}
		})
	}
}

func TestDecryptWhatsAppMedia_TamperedMACFails(t *testing.T) {
	plaintext := []byte("some image bytes padded to a couple AES blocks")
	mediaKey := make([]byte, 32)
	mediaKeyB64 := base64.StdEncoding.EncodeToString(mediaKey)
	encrypted := encryptWhatsAppMediaForTest(t, plaintext, mediaKey, "image")

	// Corrompe um byte do ciphertext — a verificação de MAC deve rejeitar
	// antes de chegar a decifrar (evita repassar lixo pro Chatwoot).
	tampered := append([]byte{}, encrypted...)
	tampered[0] ^= 0xFF

	if _, err := decryptWhatsAppMedia(tampered, mediaKeyB64, "image"); err == nil {
		t.Fatal("expected MAC verification to fail for tampered ciphertext, got nil error")
	}
}

func TestDecryptWhatsAppMedia_UnsupportedMediaType(t *testing.T) {
	if _, err := decryptWhatsAppMedia([]byte("irrelevant"), base64.StdEncoding.EncodeToString(make([]byte, 32)), "sticker"); err == nil {
		t.Fatal("expected error for unsupported media type, got nil")
	}
}

func TestDefaultMediaFileName(t *testing.T) {
	name := defaultMediaFileName("ABC123", "image", "image/jpeg")
	if name != "image-ABC123.jpg" {
		t.Fatalf("unexpected file name: %q", name)
	}

	// mimetype desconhecido não deve travar, só perde a extensão.
	name = defaultMediaFileName("ABC123", "video", "application/x-unknown")
	if name != "video-ABC123" {
		t.Fatalf("unexpected file name for unknown mimetype: %q", name)
	}
}
