package services

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// waMediaHKDFInfo mapeia o MediaType do webhook Evolution (whatsmeow) para o
// "info" usado na expansão HKDF das chaves de mídia do WhatsApp. Vídeo e gif
// compartilham a mesma info: um "gif" no WhatsApp é sempre um videoMessage
// com gifPlayback=true, não um sub-tipo de mídia próprio.
var waMediaHKDFInfo = map[string]string{
	"image":    "WhatsApp Image Keys",
	"video":    "WhatsApp Video Keys",
	"gif":      "WhatsApp Video Keys",
	"audio":    "WhatsApp Audio Keys",
	"ptt":      "WhatsApp Audio Keys",
	"document": "WhatsApp Document Keys",
	// sticker fica de fora de propósito: o webhook Evolution/whatsmeow não
	// expõe stickerMessage em EvolutionMessageBody, então mesmo com a info
	// HKDF certa não haveria conteúdo pra decifrar — ver MediaContent().
}

// decryptWhatsAppMedia reverte a cifragem que o WhatsApp aplica a mídia antes
// de subir para o CDN (mmg.whatsapp.net). O algoritmo é o mesmo em todo o
// ecossistema WhatsApp (Baileys, whatsmeow, WhatsApp Web): a mediaKey (32
// bytes) é expandida via HKDF-SHA256 (salt zerado, info específica do tipo de
// mídia) em 112 bytes — iv (16) + chave AES (32) + chave HMAC (32) + refKey
// (32, não usado aqui). O blob baixado é AES-256-CBC(iv, chave) + 10 bytes de
// HMAC-SHA256(iv||ciphertext) truncado, que validamos antes de decifrar.
//
// fileEncSHA256B64/fileSHA256B64 são os hashes que o próprio WhatsApp inclui
// no payload (fileEncSha256 = SHA-256 do blob cifrado, fileSha256 = SHA-256
// do arquivo já decifrado) — conferidos aqui contra o que baixamos/produzimos
// para distinguir, sem ambiguidade, download truncado/corrompido (o
// primeiro) de um bug na decriptação em si (o segundo), em vez de deixar um
// arquivo quebrado seguir adiante silenciosamente e só quebrar no player.
// Vazios (payload sem o campo) pulam a respectiva checagem.
func decryptWhatsAppMedia(ciphertext []byte, mediaKeyB64, waMediaType, fileEncSHA256B64, fileSHA256B64 string) ([]byte, error) {
	info, ok := waMediaHKDFInfo[waMediaType]
	if !ok {
		return nil, fmt.Errorf("no HKDF info known for media type %q", waMediaType)
	}
	if mediaKeyB64 == "" {
		return nil, fmt.Errorf("empty mediaKey")
	}
	mediaKey, err := base64.StdEncoding.DecodeString(mediaKeyB64)
	if err != nil {
		return nil, fmt.Errorf("invalid mediaKey base64: %w", err)
	}
	const macLength = 10
	if len(ciphertext) <= macLength {
		return nil, fmt.Errorf("encrypted media too short: %d bytes", len(ciphertext))
	}

	if err := verifySHA256(ciphertext, fileEncSHA256B64, "downloaded ciphertext doesn't match fileEncSha256 — download is truncated or corrupted"); err != nil {
		return nil, err
	}

	expanded := hkdfSHA256(mediaKey, []byte(info), 112)
	iv := expanded[0:16]
	cipherKey := expanded[16:48]
	macKey := expanded[48:80]

	fileCiphertext := ciphertext[:len(ciphertext)-macLength]
	receivedMAC := ciphertext[len(ciphertext)-macLength:]

	mac := hmac.New(sha256.New, macKey)
	mac.Write(iv)
	mac.Write(fileCiphertext)
	expectedMAC := mac.Sum(nil)[:macLength]
	if !hmac.Equal(receivedMAC, expectedMAC) {
		return nil, fmt.Errorf("media MAC verification failed (corrupted download or wrong mediaKey)")
	}

	if len(fileCiphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("encrypted media length %d is not a multiple of the AES block size", len(fileCiphertext))
	}
	block, err := aes.NewCipher(cipherKey)
	if err != nil {
		return nil, fmt.Errorf("failed to init AES cipher: %w", err)
	}
	plaintext := make([]byte, len(fileCiphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, fileCiphertext)

	plaintext, err = pkcs7Unpad(plaintext)
	if err != nil {
		return nil, err
	}

	if err := verifySHA256(plaintext, fileSHA256B64, "decrypted output doesn't match fileSha256 — decryption produced the wrong bytes"); err != nil {
		return nil, err
	}
	return plaintext, nil
}

// verifySHA256 confere data contra um hash SHA-256 em base64 vindo do
// payload do WhatsApp. hashB64 vazio (campo ausente no payload) pula a
// checagem em vez de falhar.
func verifySHA256(data []byte, hashB64, mismatchMsg string) error {
	if hashB64 == "" {
		return nil
	}
	want, err := base64.StdEncoding.DecodeString(hashB64)
	if err != nil {
		return fmt.Errorf("invalid sha256 base64 in payload: %w", err)
	}
	got := sha256.Sum256(data)
	if !bytes.Equal(got[:], want) {
		return fmt.Errorf("%s (got %d bytes, sha256=%x, want=%x)", mismatchMsg, len(data), got[:], want)
	}
	return nil
}

// hkdfSHA256 implementa HKDF-Extract-and-Expand (RFC 5869) com SHA-256, sem
// depender de golang.org/x/crypto/hkdf — evita puxar mais uma dependência
// externa para um algoritmo de ~15 linhas. Salt vazio (extract usa uma chave
// HMAC de HashSize zeros), como o WhatsApp faz.
func hkdfSHA256(secret, info []byte, length int) []byte {
	extractor := hmac.New(sha256.New, make([]byte, sha256.Size))
	extractor.Write(secret)
	prk := extractor.Sum(nil)

	var (
		block   []byte
		okm     []byte
		counter byte = 1
	)
	for len(okm) < length {
		mac := hmac.New(sha256.New, prk)
		mac.Write(block)
		mac.Write(info)
		mac.Write([]byte{counter})
		block = mac.Sum(nil)
		okm = append(okm, block...)
		counter++
	}
	return okm[:length]
}

// pkcs7Unpad remove o padding PKCS#7 aplicado antes da cifragem AES-CBC.
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot unpad empty data")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > len(data) || padLen > aes.BlockSize {
		return nil, fmt.Errorf("invalid PKCS7 padding")
	}
	return data[:len(data)-padLen], nil
}

// waMediaExtensions cobre os mimetypes mais comuns enviados como imagem/gif
// pelo WhatsApp. Usado só como fallback quando o payload não traz fileName
// (o caso comum para imagem — o WhatsApp normalmente só envia fileName para
// documentMessage).
var waMediaExtensions = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
	"video/mp4":       ".mp4",
	"video/3gpp":      ".3gp",
	"audio/ogg":       ".ogg",
	"audio/mpeg":      ".mp3",
	"audio/mp4":       ".m4a",
	"application/pdf": ".pdf",
}

// defaultMediaFileName gera um nome de arquivo estável quando o WhatsApp não
// envia fileName no payload (comum em imagem, vídeo e gif).
func defaultMediaFileName(messageID, waMediaType, mimetype string) string {
	ext, ok := waMediaExtensions[mimetype]
	if !ok {
		ext = ""
	}
	name := messageID
	if name == "" {
		name = waMediaType
	}
	return fmt.Sprintf("%s-%s%s", waMediaType, name, ext)
}
