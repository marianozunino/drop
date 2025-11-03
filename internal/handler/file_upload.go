package handler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/labstack/echo/v4"
	"github.com/marianozunino/drop/internal/model"
	"github.com/marianozunino/drop/internal/utils"
)

const (
	DefaultIDLength       = 16
	SecretIDLength        = 8
	ProgressChunkSizeMB   = 1
	ProgressPercentStep   = 1
	ManagementTokenLength = 16
)

var filenameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]`) // allow only safe chars

func (h *Handler) HandleUpload(c echo.Context) error {
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, h.cfg.MaxSizeToBytes())

	if err := h.parseRequestForm(c); err != nil {
		log.Printf("[HandleUpload] Failed to parse form: %v", err)
		return c.String(http.StatusBadRequest, "Invalid request form.")
	}

	if _, exists := c.Request().Form["shorten"]; exists {
		if !h.cfg.URLShorteningEnabled {
			return c.String(http.StatusBadRequest, "URL shortening feature is disabled")
		}
		return h.HandleURLShortening(c)
	}

	fileInfo, err := h.extractFileContent(c)
	if err != nil {
		log.Printf("[HandleUpload] Failed to extract file content: %v", err)
		return c.String(http.StatusBadRequest, "Failed to extract file from request.")
	}

	if fileInfo.Size == 0 {
		return c.String(http.StatusBadRequest, "Empty file")
	}

	if fileInfo.Size > h.cfg.MaxSizeToBytes() {
		return c.String(http.StatusBadRequest,
			fmt.Sprintf("File too large (max %d bytes)", h.cfg.MaxSizeToBytes()))
	}

	expirationDate, err := h.determineExpiration(c, fileInfo.Size)
	if err != nil {
		log.Printf("[HandleUpload] Invalid expiration format: %v", err)
		return c.String(http.StatusBadRequest, "Invalid expiration format.")
	}

	_, oneTimeView := c.Request().Form["one_time"]

	managementToken, err := h.storeFileMetadata(fileInfo.FilePath, fileInfo.OriginalFilename, fileInfo, expirationDate, oneTimeView, c)
	if err != nil {
		log.Printf("[HandleUpload] Failed to store metadata: %v", err)
		// Clean up the file if metadata storage fails
		if removeErr := os.Remove(fileInfo.FilePath); removeErr != nil {
			log.Printf("[HandleUpload] Failed to clean up file after metadata error: %v", removeErr)
		}
		return c.String(http.StatusInternalServerError, "Server error")
	}

	if err := h.sendUploadResponse(c, fileInfo.StoredFilename, fileInfo.Size, managementToken, expirationDate); err != nil {
		log.Printf("[HandleUpload] Failed to send upload response: %v", err)
		if removeErr := os.Remove(fileInfo.FilePath); removeErr != nil {
			log.Printf("[HandleUpload] Failed to clean up file after response error: %v", removeErr)
		}
		return c.String(http.StatusInternalServerError, "Server error")
	}

	return nil
}

// FileInfo holds information about the uploaded file
// FilePath: Path where file was saved
// StoredFilename: Final filename (with extension)
// OriginalFilename: Name as uploaded by the user
// Size: File size in bytes
// ContentType: MIME type
type FileInfo struct {
	FilePath         string // Path where file was saved
	StoredFilename   string // Final filename (with extension)
	OriginalFilename string // Original filename from user
	Size             int64
	ContentType      string
}

func (h *Handler) extractFileContent(c echo.Context) (FileInfo, error) {
	if _, exists := c.Request().Form["shorten"]; exists {
		return FileInfo{}, fmt.Errorf("URL shortening request - handled separately")
	}

	file, header, err := c.Request().FormFile("file")
	if err == nil {
		defer file.Close()
		return h.saveFromFormFile(file, header)
	}

	return h.downloadFromURL(c)
}

func (h *Handler) saveFromFormFile(file io.Reader, header *multipart.FileHeader) (FileInfo, error) {
	useSecretId := false
	id, err := h.generateFileID(useSecretId)
	if err != nil {
		return FileInfo{}, fmt.Errorf("failed to generate ID: %w", err)
	}

	fileExt := filepath.Ext(header.Filename)
	filename := id
	if fileExt != "" {
		filename += fileExt
	}

	filePath := filepath.Join(h.cfg.UploadPath, filename)

	tmpFilePath := filePath + ".tmp"
	dst, err := os.OpenFile(tmpFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return FileInfo{}, fmt.Errorf("failed to create temp file: %w", err)
	}

	progressReader := NewSimpleProgressReader(file, header.Size, header.Filename)
	log.Printf("Starting upload: %s (%s)", header.Filename, formatBytes(header.Size))

	limitedReader := io.LimitReader(progressReader, h.cfg.MaxSizeToBytes())
	size, err := io.Copy(dst, limitedReader)

	closeErr := dst.Close()
	if err != nil {
		os.Remove(tmpFilePath)
		return FileInfo{}, fmt.Errorf("failed to save file: %w", err)
	}
	if closeErr != nil {
		os.Remove(tmpFilePath)
		return FileInfo{}, fmt.Errorf("failed to close file: %w", closeErr)
	}

	if err := os.Rename(tmpFilePath, filePath); err != nil {
		os.Remove(tmpFilePath)
		return FileInfo{}, fmt.Errorf("failed to rename temp file: %w", err)
	}

	contentType := h.detectContentType(filePath)

	fileInfo := FileInfo{
		FilePath:         filePath,
		StoredFilename:   filename,
		OriginalFilename: filenameSanitizer.ReplaceAllString(header.Filename, ""),
		Size:             size,
		ContentType:      contentType,
	}

	elapsed := time.Since(progressReader.startTime)
	avgSpeed := float64(size) / elapsed.Seconds() / 1024 / 1024
	log.Printf("✓ Upload completed: %s (%s) with ID: %s - %.2f MB/s",
		header.Filename, formatBytes(size), id, avgSpeed)
	return fileInfo, nil
}

func (h *Handler) downloadFromURL(c echo.Context) (FileInfo, error) {
	var fileInfo FileInfo

	url := c.FormValue("url")
	if url == "" {
		return fileInfo, fmt.Errorf("No file or URL provided")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("Error: Failed to download from URL: %v", err)
		return fileInfo, fmt.Errorf("Failed to download from URL")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Error: URL returned non-200 status: %d", resp.StatusCode)
		return fileInfo, fmt.Errorf("URL returned status %d", resp.StatusCode)
	}

	if err := h.checkContentLength(resp, h.cfg.MaxSizeToBytes()); err != nil {
		return fileInfo, err
	}

	useSecretId := c.FormValue("secret") != ""
	id, err := h.generateFileID(useSecretId)
	if err != nil {
		return fileInfo, fmt.Errorf("failed to generate ID: %w", err)
	}

	originalName := h.extractFilenameFromURL(url)
	originalName = filenameSanitizer.ReplaceAllString(originalName, "")
	fileExt := filepath.Ext(originalName)
	filename := id
	if fileExt != "" {
		filename += fileExt
	}

	filePath := filepath.Join(h.cfg.UploadPath, filename)

	dst, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fileInfo, fmt.Errorf("failed to create file: %w", err)
	}
	defer dst.Close()

	contentLength := max(resp.ContentLength, 0)

	progressReader := NewSimpleProgressReader(resp.Body, contentLength, originalName)
	log.Printf("Starting download: %s (%s)", originalName, formatBytes(contentLength))

	limitedReader := io.LimitReader(progressReader, h.cfg.MaxSizeToBytes())
	size, err := io.Copy(dst, limitedReader)
	if err != nil {
		os.Remove(filePath)
		log.Printf("Error: Failed to save from URL: %v", err)
		return fileInfo, fmt.Errorf("failed to save from URL")
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = h.detectContentType(filePath)
	}

	fileInfo = FileInfo{
		FilePath:         filePath,
		StoredFilename:   filename,
		OriginalFilename: originalName,
		Size:             size,
		ContentType:      contentType,
	}

	log.Printf("✓ Download completed: %s (%d bytes) with ID: %s", originalName, size, id)
	return fileInfo, nil
}

func (h *Handler) detectContentType(filePath string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()

	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "application/octet-stream"
	}
	mtype := mimetype.Detect(buffer[:n])

	if mtype.String() == "" {
		return "application/octet-stream"
	}

	return mtype.String()
}

func (h *Handler) checkContentLength(resp *http.Response, maxSize int64) error {
	contentLength := resp.Header.Get("Content-Length")
	if contentLength != "" {
		length, err := strconv.ParseInt(contentLength, 10, 64)
		if err != nil {
			log.Printf("Warning: Invalid Content-Length: %v", err)
		} else if length > maxSize {
			return fmt.Errorf("File too large (max %d bytes)", maxSize)
		}
	}
	return nil
}

func (h *Handler) extractFilenameFromURL(url string) string {
	fileName := "download"
	urlPath := strings.Split(url, "/")

	if len(urlPath) > 0 && urlPath[len(urlPath)-1] != "" {
		fileName = urlPath[len(urlPath)-1]
		if idx := strings.IndexByte(fileName, '?'); idx > 0 {
			fileName = fileName[:idx]
		}
	}

	return fileName
}

func (h *Handler) generateFileID(useSecretId bool) (string, error) {
	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		var length int
		if useSecretId {
			length = 8
		} else {
			length = h.cfg.IdLength
		}

		id, err := generateID(length)
		if err != nil {
			return "", err
		}

		// Check if ID already exists
		_, err = h.db.GetMetadataByID(id)
		if err != nil {
			// ID doesn't exist, we can use it
			return id, nil
		}

		// ID exists, try again
		log.Printf("[generateFileID] Collision detected for ID %s, retrying...", id)
	}

	return "", fmt.Errorf("failed to generate unique ID after %d retries", maxRetries)
}

func (h *Handler) determineExpiration(c echo.Context, fileSize int64) (time.Time, error) {
	expiresStr := c.FormValue("expires")
	if expiresStr != "" {
		expirationDate, err := utils.ParseExpirationTime(expiresStr)
		if err != nil {
			return expirationDate, err
		}

		maxExpiration := h.expManager.GetExpirationDate(fileSize)
		log.Printf("Requested expiration date: %v", expirationDate)

		if expirationDate.After(maxExpiration) {
			log.Printf("Warning: Expiration date is too far in the future, using max expiration set by retention policy")
			return maxExpiration, nil
		} else if expirationDate.Before(time.Now()) {
			log.Printf("Warning: Expiration date is in the past, using max expiration set by retention policy")
			return maxExpiration, nil
		} else {
			log.Printf("Expiration date: %v", expirationDate)
			return expirationDate, nil
		}

	}

	expirationDate := h.expManager.GetExpirationDate(fileSize)
	return expirationDate, nil
}

func (h *Handler) storeFileMetadata(filePath, fileName string, fileInfo FileInfo, expirationDate time.Time, oneTimeView bool, c echo.Context) (string, error) {
	managementToken, err := generateID(16)
	if err != nil {
		log.Printf("Warning: Failed to generate management token: %v", err)
		managementToken = filepath.Base(filePath)
	}

	var ipAddress string
	if h.cfg.IPTrackingEnabled {
		ipAddress = c.RealIP()
	}

	metadata := model.FileMetadata{
		ResourcePath: filePath,
		Token:        managementToken,
		OriginalName: fileName,
		UploadDate:   time.Now(),
		Size:         fileInfo.Size,
		ContentType:  fileInfo.ContentType,
		OneTimeView:  oneTimeView,
		AccessCount:  0,
		IPAddress:    ipAddress,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if !expirationDate.IsZero() {
		metadata.ExpiresAt = &expirationDate
	}

	if err := h.db.StoreMetadata(&metadata); err != nil {
		return "", err
	}

	return managementToken, nil
}

func (h *Handler) sendUploadResponse(c echo.Context, filename string, fileSize int64, token string, expirationDate time.Time) error {
	c.Response().Header().Set("X-Token", token)
	fileURL := h.expManager.Config.BaseURL + filename

	if !expirationDate.IsZero() {
		expiresMs := expirationDate.UnixNano() / int64(time.Millisecond)
		c.Response().Header().Set("X-Expires", fmt.Sprintf("%d", expiresMs))
	}

	// Calculate MD5 hash
	filePath := filepath.Join(h.cfg.UploadPath, filename)
	md5Hash, err := utils.CalculateMD5(filePath)
	if err != nil {
		log.Printf("Warning: Failed to calculate MD5 for %s: %v", filename, err)
		md5Hash = "" // Set empty string if calculation fails
	}

	if strings.Contains(c.Request().Header.Get("Accept"), "application/json") {
		response := map[string]any{
			"url":   fileURL,
			"size":  fileSize,
			"token": token,
			"md5":   md5Hash,
		}

		if !expirationDate.IsZero() {
			response["expires_at"] = expirationDate.Format(time.RFC3339)
			days := int(time.Until(expirationDate).Hours() / 24)
			response["expires_in_days"] = days
		}

		return c.JSON(http.StatusOK, response)
	}

	c.Response().Header().Set("Content-Type", "text/plain; charset=utf-8")
	return c.String(http.StatusOK, fileURL+"\n")
}

func generateID(length int) (string, error) {
	bytes := make([]byte, length/2+1)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes)[:length], nil
}
