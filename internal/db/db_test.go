package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marianozunino/drop/internal/config"
	"github.com/marianozunino/drop/internal/model"
	"github.com/marianozunino/drop/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) (*DB, func()) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	cfg := &config.Config{
		SQLitePath: dbPath,
	}

	db, err := NewDB(cfg)
	require.NoError(t, err)

	// Run migrations for tests
	err = testutil.RunTestMigrations(dbPath)
	require.NoError(t, err)

	cleanup := func() {
		db.Close()
		os.RemoveAll(tempDir)
	}

	return db, cleanup
}

func TestNewDB(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "new_test.db")

	cfg := &config.Config{
		SQLitePath: dbPath,
	}

	db, err := NewDB(cfg)
	require.NoError(t, err)
	defer db.Close()

	_, err = os.Stat(dbPath)
	assert.NoError(t, err)

	err = db.Ping()
	assert.NoError(t, err)
}

func TestNewDBWithInvalidPath(t *testing.T) {
	cfg := &config.Config{
		SQLitePath: "/invalid/path/that/does/not/exist/test.db",
	}

	db, err := NewDB(cfg)
	assert.Error(t, err)
	assert.Nil(t, db)
}

func TestStoreMetadata(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)

	metadata := &model.FileMetadata{
		ResourcePath: "/uploads/test-file.txt",
		Token:        "test-token-123",
		OriginalName: "original-file.txt",
		UploadDate:   now,
		ExpiresAt:    &expiresAt,
		Size:         1024,
		ContentType:  "text/plain",
		OneTimeView:  true,
	}

	err := db.StoreMetadata(metadata)
	assert.NoError(t, err)
}

func TestStoreMetadataWithInvalidJSON(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	metadata := &model.FileMetadata{
		ResourcePath: "/uploads/test-file.txt",
		Token:        "test-token",
		Size:         1024,
	}

	err := db.StoreMetadata(metadata)
	assert.NoError(t, err)
}

func TestGetMetadataByID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)

	originalMetadata := &model.FileMetadata{
		ResourcePath: "/uploads/test-file.txt",
		Token:        "test-token-123",
		OriginalName: "original-file.txt",
		UploadDate:   now,
		ExpiresAt:    &expiresAt,
		Size:         1024,
		ContentType:  "text/plain",
		OneTimeView:  true,
	}

	err := db.StoreMetadata(originalMetadata)
	require.NoError(t, err)

	retrievedMetadata, err := db.GetMetadataByID(originalMetadata.ID())
	require.NoError(t, err)

	// Verify the retrieved metadata matches the original
	assert.Equal(t, originalMetadata.ResourcePath, retrievedMetadata.ResourcePath)
	assert.Equal(t, originalMetadata.Token, retrievedMetadata.Token)
	assert.Equal(t, originalMetadata.OriginalName, retrievedMetadata.OriginalName)
	assert.Equal(t, originalMetadata.UploadDate.Unix(), retrievedMetadata.UploadDate.Unix())
	assert.Equal(t, originalMetadata.ExpiresAt.Unix(), retrievedMetadata.ExpiresAt.Unix())
	assert.Equal(t, originalMetadata.Size, retrievedMetadata.Size)
	assert.Equal(t, originalMetadata.ContentType, retrievedMetadata.ContentType)
	assert.Equal(t, originalMetadata.OneTimeView, retrievedMetadata.OneTimeView)
}

func TestGetMetadataByIDNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	metadata, err := db.GetMetadataByID("non-existent-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no metadata found with ID")
	assert.Empty(t, metadata.ResourcePath)
}

func TestMetadataConsistencyAfterUpdate(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	originalExpiresAt := now.Add(24 * time.Hour)
	newExpiresAt := now.Add(48 * time.Hour)

	originalMetadata := &model.FileMetadata{
		ResourcePath:   "test123",
		Token:          "token-123",
		OriginalName:   "URL Shortener",
		UploadDate:     now,
		ExpiresAt:      &originalExpiresAt,
		Size:           0,
		ContentType:    "text/html",
		OneTimeView:    false,
		OriginalURL:    "https://www.example.com",
		IsURLShortener: true,
		AccessCount:    0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	err := db.StoreMetadata(originalMetadata)
	require.NoError(t, err)

	metadata1, err := db.GetMetadataByID("test123")
	require.NoError(t, err)
	assert.Equal(t, "test123", metadata1.ResourcePath)
	assert.Equal(t, originalExpiresAt.Unix(), metadata1.ExpiresAt.Unix())

	updatedMetadata := model.FileMetadata{
		ResourcePath:   "test123",
		Token:          "token-123",
		OriginalName:   "URL Shortener",
		UploadDate:     now,
		ExpiresAt:      &newExpiresAt,
		Size:           0,
		ContentType:    "text/html",
		OneTimeView:    true,
		OriginalURL:    "https://www.example.com/updated",
		IsURLShortener: true,
		AccessCount:    5,
		CreatedAt:      now,
		UpdatedAt:      now.Add(time.Hour),
	}

	err = db.StoreMetadata(&updatedMetadata)
	require.NoError(t, err)

	var dbId, dbResourcePath string
	err = db.DB.QueryRow("SELECT id, resource_path FROM metadata WHERE id = ?", "test123").Scan(&dbId, &dbResourcePath)
	require.NoError(t, err)
	assert.Equal(t, "test123", dbId, "Database id should be 'test123'")
	assert.Equal(t, "test123", dbResourcePath, "Database resource_path should be 'test123'")
	assert.Equal(t, dbId, dbResourcePath, "id and resource_path must always match")

	metadata2, err := db.GetMetadataByID("test123")
	require.NoError(t, err)
	assert.Equal(t, "test123", metadata2.ResourcePath, "ResourcePath should still be 'test123'")
	assert.Equal(t, newExpiresAt.Unix(), metadata2.ExpiresAt.Unix(), "Expiration should be updated")
	assert.True(t, metadata2.OneTimeView, "OneTimeView should be updated to true")
	assert.Equal(t, "https://www.example.com/updated", metadata2.OriginalURL, "OriginalURL should be updated")
	assert.Equal(t, 5, metadata2.AccessCount, "AccessCount should be updated")

	metadata3, err := db.GetMetadataByID("test123")
	require.NoError(t, err)
	assert.Equal(t, "test123", metadata3.ResourcePath)
}

func TestStoreMetadataEnforcesIdResourcePathConsistency(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)

	metadata1 := &model.FileMetadata{
		ResourcePath:   "abc123",
		Token:          "token-abc",
		OriginalName:   "Test File 1",
		UploadDate:     now,
		ExpiresAt:      &expiresAt,
		Size:           1024,
		ContentType:    "text/plain",
		OneTimeView:    false,
		IsURLShortener: false,
		AccessCount:    0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	err := db.StoreMetadata(metadata1)
	require.NoError(t, err)

	var dbId, dbResourcePath string
	err = db.DB.QueryRow("SELECT id, resource_path FROM metadata WHERE id = ?", "abc123").Scan(&dbId, &dbResourcePath)
	require.NoError(t, err)
	assert.Equal(t, "abc123", dbId, "Database id must match ResourcePath")
	assert.Equal(t, "abc123", dbResourcePath, "Database resource_path must match ResourcePath")
	assert.Equal(t, dbId, dbResourcePath, "id and resource_path must always be identical after StoreMetadata")

	_, err = db.DB.Exec(`
		INSERT INTO metadata (
			id, resource_path, token, original_name, upload_date, expires_at, 
			size, content_type, one_time_view, original_url, is_url_shortener,
			access_count, ip_address, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "mismatch_id", "different_path", "token-mismatch", "Mismatch Test", now, nil, 0, "text/plain", false, "", false, 0, "", now, now)
	require.NoError(t, err)

	count := 0
	err = db.DB.QueryRow("SELECT COUNT(*) FROM metadata WHERE id != resource_path").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "We manually created one inconsistent record")

	metadata2 := &model.FileMetadata{
		ResourcePath:   "xyz789",
		Token:          "token-xyz",
		OriginalName:   "Test File 2",
		UploadDate:     now,
		ExpiresAt:      &expiresAt,
		Size:           2048,
		ContentType:    "application/json",
		OneTimeView:    true,
		IsURLShortener: false,
		AccessCount:    0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	err = db.StoreMetadata(metadata2)
	require.NoError(t, err)

	err = db.DB.QueryRow("SELECT id, resource_path FROM metadata WHERE id = ?", "xyz789").Scan(&dbId, &dbResourcePath)
	require.NoError(t, err)
	assert.Equal(t, "xyz789", dbId, "StoreMetadata must ensure id matches ResourcePath")
	assert.Equal(t, "xyz789", dbResourcePath, "StoreMetadata must ensure resource_path matches ResourcePath")
	assert.Equal(t, dbId, dbResourcePath, "StoreMetadata must enforce id == resource_path")

	metadata3 := &model.FileMetadata{
		ResourcePath:   "url456",
		Token:          "token-url",
		OriginalName:   "URL Shortener",
		UploadDate:     now,
		ExpiresAt:      &expiresAt,
		Size:           0,
		ContentType:    "text/html",
		OneTimeView:    false,
		OriginalURL:    "https://example.com",
		IsURLShortener: true,
		AccessCount:    0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	err = db.StoreMetadata(metadata3)
	require.NoError(t, err)

	err = db.DB.QueryRow("SELECT id, resource_path FROM metadata WHERE id = ?", "url456").Scan(&dbId, &dbResourcePath)
	require.NoError(t, err)
	assert.Equal(t, "url456", dbId, "For URL shorteners, id must match ResourcePath")
	assert.Equal(t, "url456", dbResourcePath, "For URL shorteners, resource_path must match ResourcePath")
	assert.Equal(t, dbId, dbResourcePath, "For URL shorteners, id and resource_path must always match")

	count = 0
	err = db.DB.QueryRow("SELECT COUNT(*) FROM metadata WHERE id != resource_path").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "Only the manually created inconsistent record should exist. All StoreMetadata calls must create consistent records.")

	rows, err := db.DB.Query("SELECT id, resource_path FROM metadata WHERE id = resource_path")
	require.NoError(t, err)
	defer rows.Close()

	consistentCount := 0
	for rows.Next() {
		var id, path string
		err = rows.Scan(&id, &path)
		require.NoError(t, err)
		assert.Equal(t, id, path, "Every record created by StoreMetadata must have id == resource_path")
		consistentCount++
	}
	require.NoError(t, rows.Err())
	assert.GreaterOrEqual(t, consistentCount, 3, "At least 3 consistent records should exist (abc123, xyz789, url456)")

	var allIds, allPaths []string
	rows2, err := db.DB.Query("SELECT id, resource_path FROM metadata ORDER BY id")
	require.NoError(t, err)
	defer rows2.Close()

	for rows2.Next() {
		var id, path string
		err = rows2.Scan(&id, &path)
		require.NoError(t, err)
		allIds = append(allIds, id)
		allPaths = append(allPaths, path)
	}
	require.NoError(t, rows2.Err())

	mismatches := 0
	for i := range allIds {
		if allIds[i] != allPaths[i] {
			mismatches++
			t.Logf("Mismatch found: id=%s, resource_path=%s", allIds[i], allPaths[i])
		}
	}
	assert.Equal(t, 1, mismatches, "Only the manually inserted inconsistent record should have a mismatch. This test verifies StoreMetadata enforces consistency.")

	updateMetadata := &model.FileMetadata{
		ResourcePath:   "mismatch_id",
		Token:          "token-updated",
		OriginalName:   "Updated File",
		UploadDate:     now,
		ExpiresAt:      nil,
		Size:           4096,
		ContentType:    "application/pdf",
		OneTimeView:    false,
		IsURLShortener: false,
		AccessCount:    5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	err = db.StoreMetadata(updateMetadata)
	require.NoError(t, err)

	err = db.DB.QueryRow("SELECT id, resource_path FROM metadata WHERE id = ?", "mismatch_id").Scan(&dbId, &dbResourcePath)
	require.NoError(t, err)
	assert.Equal(t, "mismatch_id", dbId, "After INSERT OR REPLACE, id must equal the ResourcePath used")
	assert.Equal(t, "mismatch_id", dbResourcePath, "After INSERT OR REPLACE, resource_path must equal ResourcePath")
	assert.Equal(t, dbId, dbResourcePath, "INSERT OR REPLACE must ensure id == resource_path even when replacing existing record")

	count = 0
	err = db.DB.QueryRow("SELECT COUNT(*) FROM metadata WHERE id != resource_path").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "After INSERT OR REPLACE of the inconsistent record, all records must be consistent. This proves StoreMetadata enforces consistency even when replacing existing records.")
}

func TestListAllMetadata(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	metadata1 := &model.FileMetadata{
		ResourcePath: "/uploads/file1.txt",
		Token:        "token1",
		Size:         1024,
	}

	metadata2 := &model.FileMetadata{
		ResourcePath: "/uploads/file2.txt",
		Token:        "token2",
		Size:         2048,
	}

	metadata3 := &model.FileMetadata{
		ResourcePath: "/uploads/file3.txt",
		Token:        "token3",
		Size:         4096,
	}

	err := db.StoreMetadata(metadata1)
	require.NoError(t, err)

	err = db.StoreMetadata(metadata2)
	require.NoError(t, err)

	err = db.StoreMetadata(metadata3)
	require.NoError(t, err)

	allMetadata, err := db.ListAllMetadata()
	require.NoError(t, err)

	assert.Len(t, allMetadata, 3)

	filePaths := make(map[string]bool)
	for _, meta := range allMetadata {
		filePaths[meta.ResourcePath] = true
	}

	assert.True(t, filePaths["/uploads/file1.txt"])
	assert.True(t, filePaths["/uploads/file2.txt"])
	assert.True(t, filePaths["/uploads/file3.txt"])
}

func TestListAllMetadataEmpty(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	allMetadata, err := db.ListAllMetadata()
	require.NoError(t, err)

	assert.Empty(t, allMetadata)
}

func TestDeleteMetadata(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	metadata := &model.FileMetadata{
		ResourcePath: "/uploads/file-to-delete.txt",
		Token:        "delete-token",
		Size:         1024,
	}

	err := db.StoreMetadata(metadata)
	require.NoError(t, err)

	retrievedMetadata, err := db.GetMetadataByID(metadata.ID())
	require.NoError(t, err)
	assert.Equal(t, metadata.ResourcePath, retrievedMetadata.ResourcePath)

	err = db.DeleteMetadata(metadata)
	assert.NoError(t, err)

	_, err = db.GetMetadataByID(metadata.ID())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no metadata found with ID")
}

func TestDeleteMetadataNonExistent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	metadata := &model.FileMetadata{
		ResourcePath: "/uploads/non-existent.txt",
		Token:        "non-existent-token",
		Size:         1024,
	}

	err := db.DeleteMetadata(metadata)
	assert.NoError(t, err)
}

func TestStoreMetadataReplace(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	originalMetadata := &model.FileMetadata{
		ResourcePath: "/uploads/same-file.txt",
		Token:        "original-token",
		Size:         1024,
	}

	err := db.StoreMetadata(originalMetadata)
	require.NoError(t, err)

	updatedMetadata := &model.FileMetadata{
		ResourcePath: "/uploads/same-file.txt",
		Token:        "updated-token",
		Size:         2048,
	}

	err = db.StoreMetadata(updatedMetadata)
	require.NoError(t, err)

	retrievedMetadata, err := db.GetMetadataByID(originalMetadata.ID())
	require.NoError(t, err)

	assert.Equal(t, updatedMetadata.Token, retrievedMetadata.Token)
	assert.Equal(t, updatedMetadata.Size, retrievedMetadata.Size)
	assert.NotEqual(t, originalMetadata.Token, retrievedMetadata.Token)
}

func TestClose(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	err := db.Ping()
	assert.NoError(t, err)

	err = db.Close()
	assert.NoError(t, err)

	err = db.Ping()
	assert.Error(t, err)
}

func TestConcurrentOperations(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(index int) {
			metadata := &model.FileMetadata{
				ResourcePath: filepath.Join("/uploads", "concurrent", "file"+string(rune(index))+".txt"),
				Token:        "token" + string(rune(index)),
				Size:         int64(1024 * index),
			}

			err := db.StoreMetadata(metadata)
			assert.NoError(t, err)

			retrievedMetadata, err := db.GetMetadataByID(metadata.ID())
			assert.NoError(t, err)
			assert.Equal(t, metadata.ResourcePath, retrievedMetadata.ResourcePath)

			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	allMetadata, err := db.ListAllMetadata()
	require.NoError(t, err)
	assert.Len(t, allMetadata, 10)
}
