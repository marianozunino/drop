package handler

import (
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/marianozunino/drop/internal/config"
	"github.com/marianozunino/drop/internal/model"
	"github.com/marianozunino/drop/internal/utils"
)

// ChunkedUpload handles resumable file uploads
type ChunkedUpload struct {
	UploadID       string       `json:"upload_id"`
	Filename       string       `json:"filename"`
	TotalSize      int64        `json:"total_size"`
	ChunkSize      int64        `json:"chunk_size"`
	TotalChunks    int          `json:"total_chunks"`
	UploadedChunks map[int]bool `json:"uploaded_chunks"`
	CreatedAt      time.Time    `json:"created_at"`
	ExpiresAt      time.Time    `json:"expires_at"`
	mu             sync.RWMutex
}

// ChunkedUploadManager manages chunked uploads
type ChunkedUploadManager struct {
	uploads map[string]*ChunkedUpload
	mu      sync.RWMutex
	cfg     *config.Config
}

// NewChunkedUploadManager creates a new chunked upload manager
func NewChunkedUploadManager(cfg *config.Config) *ChunkedUploadManager {
	return &ChunkedUploadManager{
		uploads: make(map[string]*ChunkedUpload),
		cfg:     cfg,
	}
}

// InitiateChunkedUpload starts a new chunked upload session
func (h *Handler) InitiateChunkedUpload(c echo.Context) error {
	filename := c.FormValue("filename")
	totalSize, err := strconv.ParseInt(c.FormValue("size"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid size parameter"})
	}

	if totalSize > h.cfg.MaxSizeToBytes() {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "File too large"})
	}

	uploadID, err := h.generateFileID(false)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate upload ID"})
	}

	chunkSize := h.cfg.ChunkSizeToBytes()
	if customChunkSize, err := strconv.ParseInt(c.FormValue("chunk_size"), 10, 64); err == nil && customChunkSize > 0 {
		chunkSize = customChunkSize
	}

	totalChunks := int((totalSize + chunkSize - 1) / chunkSize)

	upload := &ChunkedUpload{
		UploadID:       uploadID,
		Filename:       filename,
		TotalSize:      totalSize,
		ChunkSize:      chunkSize,
		TotalChunks:    totalChunks,
		UploadedChunks: make(map[int]bool),
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}

	h.chunkedManager.mu.Lock()
	h.chunkedManager.uploads[uploadID] = upload
	h.chunkedManager.mu.Unlock()

	uploadDir := filepath.Join(h.cfg.UploadPath, uploadID)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to create upload directory"})
	}

	log.Printf("Starting chunked upload: %s (%s) - %d chunks of %s each",
		filename, formatBytes(totalSize), totalChunks, formatBytes(chunkSize))

	return c.JSON(http.StatusOK, map[string]interface{}{
		"upload_id":       uploadID,
		"chunk_size":      chunkSize,
		"total_chunks":    totalChunks,
		"uploaded_chunks": []int{},
	})
}

// UploadChunk handles individual chunk uploads
func (h *Handler) UploadChunk(c echo.Context) error {
	uploadID := c.Param("upload_id")
	chunkIndex, err := strconv.Atoi(c.Param("chunk"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid chunk index"})
	}

	h.chunkedManager.mu.RLock()
	upload, exists := h.chunkedManager.uploads[uploadID]
	h.chunkedManager.mu.RUnlock()

	if !exists {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Upload session not found"})
	}

	if time.Now().After(upload.ExpiresAt) {
		h.cleanupChunkedUpload(uploadID)
		return c.JSON(http.StatusGone, map[string]string{"error": "Upload session expired"})
	}

	if chunkIndex < 0 || chunkIndex >= upload.TotalChunks {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid chunk index"})
	}

	upload.mu.RLock()
	if upload.UploadedChunks[chunkIndex] {
		upload.mu.RUnlock()
		progress := h.calculateProgress(upload)
		log.Printf("Chunk %d/%d already uploaded for %s (Progress: %d%%)",
			chunkIndex+1, upload.TotalChunks, upload.Filename, progress)
		return c.JSON(http.StatusOK, map[string]interface{}{
			"message":  "Chunk already uploaded",
			"progress": progress,
		})
	}
	upload.mu.RUnlock()

	file, err := c.FormFile("chunk")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "No chunk data provided"})
	}

	// Save chunk
	chunkPath := filepath.Join(h.cfg.UploadPath, uploadID, fmt.Sprintf("chunk_%d", chunkIndex))
	if err := h.saveChunk(file, chunkPath); err != nil {
		log.Printf("Failed to save chunk %d/%d for %s: %v",
			chunkIndex+1, upload.TotalChunks, upload.Filename, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save chunk"})
	}

	upload.mu.Lock()
	upload.UploadedChunks[chunkIndex] = true
	upload.mu.Unlock()

	progress := h.calculateProgress(upload)
	log.Printf("Chunk %d/%d uploaded for %s (Progress: %d%%)",
		chunkIndex+1, upload.TotalChunks, upload.Filename, progress)

	if h.isUploadComplete(upload) {
		log.Printf("All chunks uploaded for %s, finalizing...", upload.Filename)
		managementToken, err := h.finalizeChunkedUpload(upload, c)
		if err != nil {
			log.Printf("Failed to finalize upload for %s: %v", upload.Filename, err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to finalize upload"})
		}
		log.Printf("✓ Chunked upload completed: %s (%s) with ID: %s",
			upload.Filename, formatBytes(upload.TotalSize), upload.UploadID)

		fileExt := filepath.Ext(upload.Filename)
		fileURL := h.cfg.BaseURL + upload.UploadID
		if fileExt != "" {
			fileURL += fileExt
		} else {
			fileURL += "_file"
		}

		// Calculate MD5 hash
		finalFilename := upload.UploadID
		if fileExt != "" {
			finalFilename += fileExt
		} else {
			finalFilename += "_file"
		}
		finalPath := filepath.Join(h.cfg.UploadPath, finalFilename)
		md5Hash, err := utils.CalculateMD5(finalPath)
		if err != nil {
			log.Printf("Warning: Failed to calculate MD5 for %s: %v", finalFilename, err)
			md5Hash = "" // Set empty string if calculation fails
		}

		response := map[string]interface{}{
			"message":  "Upload completed",
			"progress": 100,
			"file_url": fileURL,
			"md5":      md5Hash,
			"token":    managementToken,
		}

		// Get expiration information from stored metadata
		metadata, err := h.db.GetMetadataByID(finalPath)
		if err == nil && metadata.ExpiresAt != nil && !metadata.ExpiresAt.IsZero() {
			response["expires_at"] = metadata.ExpiresAt.Format(time.RFC3339)
			days := int(time.Until(*metadata.ExpiresAt).Hours() / 24)
			response["expires_in_days"] = days
		}

		return c.JSON(http.StatusOK, response)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Chunk uploaded successfully",
		"progress": progress,
	})
}

// GetUploadStatus returns the current status of a chunked upload
func (h *Handler) GetUploadStatus(c echo.Context) error {
	uploadID := c.Param("upload_id")

	h.chunkedManager.mu.RLock()
	upload, exists := h.chunkedManager.uploads[uploadID]
	h.chunkedManager.mu.RUnlock()

	if !exists {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Upload session not found"})
	}

	upload.mu.RLock()
	defer upload.mu.RUnlock()

	uploadedChunks := make([]int, 0, len(upload.UploadedChunks))
	for chunkIndex := range upload.UploadedChunks {
		uploadedChunks = append(uploadedChunks, chunkIndex)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"upload_id":       upload.UploadID,
		"filename":        upload.Filename,
		"total_size":      upload.TotalSize,
		"chunk_size":      upload.ChunkSize,
		"total_chunks":    upload.TotalChunks,
		"uploaded_chunks": uploadedChunks,
		"progress":        h.calculateProgress(upload),
		"created_at":      upload.CreatedAt,
		"expires_at":      upload.ExpiresAt,
	})
}

// saveChunk saves an individual chunk to disk
func (h *Handler) saveChunk(file *multipart.FileHeader, chunkPath string) error {
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(chunkPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

// isUploadComplete checks if all chunks have been uploaded
func (h *Handler) isUploadComplete(upload *ChunkedUpload) bool {
	upload.mu.RLock()
	defer upload.mu.RUnlock()
	return len(upload.UploadedChunks) == upload.TotalChunks
}

// calculateProgress calculates upload progress percentage
func (h *Handler) calculateProgress(upload *ChunkedUpload) int {
	upload.mu.RLock()
	defer upload.mu.RUnlock()
	if upload.TotalChunks == 0 {
		return 0
	}
	return int(float64(len(upload.UploadedChunks)) / float64(upload.TotalChunks) * 100)
}

// finalizeChunkedUpload combines all chunks into the final file
func (h *Handler) finalizeChunkedUpload(upload *ChunkedUpload, c echo.Context) (string, error) {
	uploadDir := filepath.Join(h.cfg.UploadPath, upload.UploadID)

	fileExt := filepath.Ext(upload.Filename)
	finalFilename := upload.UploadID
	if fileExt != "" {
		finalFilename += fileExt
	} else {
		finalFilename += "_file"
	}
	finalPath := filepath.Join(h.cfg.UploadPath, finalFilename)

	finalFile, err := os.Create(finalPath)
	if err != nil {
		return "", err
	}
	defer finalFile.Close()

	for i := 0; i < upload.TotalChunks; i++ {
		chunkPath := filepath.Join(uploadDir, fmt.Sprintf("chunk_%d", i))
		chunkFile, err := os.Open(chunkPath)
		if err != nil {
			return "", err
		}

		_, err = io.Copy(finalFile, chunkFile)
		chunkFile.Close()
		if err != nil {
			return "", err
		}
	}

	managementToken, err := h.generateFileID(false)
	if err != nil {
		log.Printf("Warning: Failed to generate management token: %v", err)
		managementToken = filepath.Base(finalPath)
	}

	contentType := h.detectContentType(finalPath)

	expirationDate := h.expManager.GetExpirationDate(upload.TotalSize)

	var ipAddress string
	if h.cfg.IPTrackingEnabled {
		ipAddress = c.RealIP()
	}

	metadata := model.FileMetadata{
		ResourcePath: finalPath,
		Token:        managementToken,
		OriginalName: filenameSanitizer.ReplaceAllString(upload.Filename, ""),
		UploadDate:   time.Now(),
		Size:         upload.TotalSize,
		ContentType:  contentType,
		OneTimeView:  false,
		AccessCount:  0,
		IPAddress:    ipAddress,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if !expirationDate.IsZero() {
		metadata.ExpiresAt = &expirationDate
	}

	if err := h.db.StoreMetadata(&metadata); err != nil {
		log.Printf("Failed to store metadata for chunked upload: %v", err)
		return "", err
	}

	os.RemoveAll(uploadDir)

	h.chunkedManager.mu.Lock()
	delete(h.chunkedManager.uploads, upload.UploadID)
	h.chunkedManager.mu.Unlock()

	return managementToken, nil
}

// cleanupChunkedUpload removes expired upload sessions
func (h *Handler) cleanupChunkedUpload(uploadID string) {
	uploadDir := filepath.Join(h.cfg.UploadPath, uploadID)
	os.RemoveAll(uploadDir)

	h.chunkedManager.mu.Lock()
	delete(h.chunkedManager.uploads, uploadID)
	h.chunkedManager.mu.Unlock()
}
