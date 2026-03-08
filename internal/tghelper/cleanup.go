package tghelper

import (
	"log"
	"os"
	"strings"
)

type TempCleaner interface {
	GetTempDir() string
	Cleanup(filePath string) error
}

func CleanupFile(filePath string, cleaner TempCleaner, logPrefix string) {
	if strings.TrimSpace(filePath) == "" {
		return
	}

	if cleaner != nil && strings.HasPrefix(filePath, cleaner.GetTempDir()) {
		if err := cleaner.Cleanup(filePath); err != nil && !os.IsNotExist(err) {
			log.Printf("[%s] Failed to cleanup temp file %s: %v", logPrefix, filePath, err)
		}
		return
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		log.Printf("[%s] Failed to delete file %s: %v", logPrefix, filePath, err)
	}
}
