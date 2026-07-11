package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type MediaService struct {
	tempDir string
}

func NewMediaService(tempDir string) *MediaService {
	// Garante que o diretório temporário exista
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		zap.L().Fatal("failed to create media temp dir", zap.String("dir", tempDir), zap.Error(err))
	}
	return &MediaService{tempDir: tempDir}
}

func (s *MediaService) DownloadFile(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download media, status code: %d", resp.StatusCode)
	}

	fileName := fmt.Sprintf("%s.tmp", uuid.New().String())
	filePath := filepath.Join(s.tempDir, fileName)

	file, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", err
	}

	return filePath, nil
}

func (s *MediaService) ReadFile(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}

func (s *MediaService) Cleanup(filePath string) {
	if err := os.Remove(filePath); err != nil {
		zap.L().Warn("failed to cleanup media file", zap.String("path", filePath), zap.Error(err))
	}
}
