package services

import (
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateMessageWithAttachment_SetsRealContentType garante que a parte do
// arquivo no multipart carregue o mimetype real (ex.: image/jpeg), não o
// application/octet-stream que mime/multipart.CreateFormFile fixaria. É esse
// Content-Type que o Chatwoot usa (via ActiveStorage) para decidir se o
// anexo renderiza como prévia de imagem/vídeo inline ou como cartão de
// arquivo genérico.
func TestCreateMessageWithAttachment_SetsRealContentType(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		fileName string
	}{
		{"image", "image/jpeg", "photo.jpg"},
		{"gif as video", "video/mp4", "clip.mp4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPartContentType string
			var gotFormValues = map[string]string{}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
					t.Errorf("expected multipart/form-data request, got Content-Type=%q err=%v", r.Header.Get("Content-Type"), err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				reader := multipart.NewReader(r.Body, params["boundary"])
				for {
					part, err := reader.NextPart()
					if err != nil {
						break
					}
					if part.FormName() == "attachments[]" {
						gotPartContentType = part.Header.Get("Content-Type")
					} else {
						buf := make([]byte, 1024)
						n, _ := part.Read(buf)
						gotFormValues[part.FormName()] = string(buf[:n])
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":42}`))
			}))
			defer srv.Close()

			client := NewChatwootAdminClient(srv.URL, "test-token")
			resp, err := client.CreateMessageWithAttachment(t.Context(), 1, 99, "a caption", []byte("fake file bytes"), tt.fileName, tt.mimeType)
			if err != nil {
				t.Fatalf("CreateMessageWithAttachment: %v", err)
			}
			if resp.ID != 42 {
				t.Fatalf("unexpected response id: %d", resp.ID)
			}
			if gotPartContentType != tt.mimeType {
				t.Fatalf("attachment part Content-Type = %q, want %q", gotPartContentType, tt.mimeType)
			}
			if gotFormValues["content"] != "a caption" {
				t.Fatalf("content field = %q, want %q", gotFormValues["content"], "a caption")
			}
			if gotFormValues["message_type"] != "incoming" {
				t.Fatalf("message_type field = %q, want %q", gotFormValues["message_type"], "incoming")
			}
		})
	}
}
