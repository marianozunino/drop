package db

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/marianozunino/drop/internal/config"
	"github.com/marianozunino/drop/internal/model"
	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	*sqlx.DB
}

type Storeable interface {
	ID() string
}

// NewDB creates a new SQLite database connection
func NewDB(config *config.Config) (*DB, error) {
	// Configure SQLite with better concurrency settings
	db, err := sqlx.Open("sqlite3", config.SQLitePath+"?_busy_timeout=30000&_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}

	// Set connection pool settings
	db.SetMaxOpenConns(1) // SQLite doesn't handle multiple connections well
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return &DB{db}, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.DB.Close()
}

// StoreMetadata stores metadata in SQLite
func (db *DB) StoreMetadata(metadata Storeable) error {
	fileMeta, ok := metadata.(*model.FileMetadata)
	if !ok {
		return fmt.Errorf("metadata must be of type *FileMetadata")
	}

	stmt, err := db.Prepare(`
		INSERT OR REPLACE INTO metadata (
			id, resource_path, token, original_name, 
			upload_date, expires_at, size, content_type, one_time_view,
			original_url, is_url_shortener, access_count, ip_address, 
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	idValue := fileMeta.ResourcePath
	if idValue == "" {
		idValue = metadata.ID()
	}

	_, err = stmt.Exec(
		idValue,
		fileMeta.ResourcePath,
		fileMeta.Token,
		fileMeta.OriginalName,
		fileMeta.UploadDate,
		fileMeta.ExpiresAt,
		fileMeta.Size,
		fileMeta.ContentType,
		fileMeta.OneTimeView,
		fileMeta.OriginalURL,
		fileMeta.IsURLShortener,
		fileMeta.AccessCount,
		fileMeta.IPAddress,
		fileMeta.CreatedAt,
		fileMeta.UpdatedAt,
	)
	return err
}

// GetMetadataByID retrieves metadata from SQLite
func (db *DB) GetMetadataByID(ID string) (model.FileMetadata, error) {
	var metadata model.FileMetadata
	var expiresAt sql.NullTime

	err := db.QueryRow(`
		SELECT resource_path, token, original_name, upload_date, expires_at, 
		       size, content_type, one_time_view, original_url, is_url_shortener,
		       access_count, ip_address, created_at, updated_at
		FROM metadata WHERE id = ?
	`, ID).Scan(
		&metadata.ResourcePath,
		&metadata.Token,
		&metadata.OriginalName,
		&metadata.UploadDate,
		&expiresAt,
		&metadata.Size,
		&metadata.ContentType,
		&metadata.OneTimeView,
		&metadata.OriginalURL,
		&metadata.IsURLShortener,
		&metadata.AccessCount,
		&metadata.IPAddress,
		&metadata.CreatedAt,
		&metadata.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return metadata, fmt.Errorf("no metadata found with ID: %s", ID)
		}
		return metadata, err
	}

	// Handle NULL expires_at
	if expiresAt.Valid {
		metadata.ExpiresAt = &expiresAt.Time
	}

	return metadata, nil
}

// GetMetadataByToken retrieves metadata from SQLite by token
func (db *DB) GetMetadataByToken(token string) (model.FileMetadata, error) {
	var metadata model.FileMetadata
	var expiresAt sql.NullTime

	err := db.QueryRow(`
		SELECT resource_path, token, original_name, upload_date, expires_at, 
		       size, content_type, one_time_view, original_url, is_url_shortener,
		       access_count, ip_address, created_at, updated_at
		FROM metadata WHERE token = ?
	`, token).Scan(
		&metadata.ResourcePath,
		&metadata.Token,
		&metadata.OriginalName,
		&metadata.UploadDate,
		&expiresAt,
		&metadata.Size,
		&metadata.ContentType,
		&metadata.OneTimeView,
		&metadata.OriginalURL,
		&metadata.IsURLShortener,
		&metadata.AccessCount,
		&metadata.IPAddress,
		&metadata.CreatedAt,
		&metadata.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return metadata, fmt.Errorf("no metadata found with token: %s", token)
		}
		return metadata, err
	}

	// Handle NULL expires_at
	if expiresAt.Valid {
		metadata.ExpiresAt = &expiresAt.Time
	}

	return metadata, nil
}

// ListAllMetadata lists all metadata
func (db *DB) ListAllMetadata() ([]model.FileMetadata, error) {
	var metadataList []model.FileMetadata

	rows, err := db.Query(`
		SELECT resource_path, token, original_name, upload_date, expires_at, 
		       size, content_type, one_time_view, original_url, is_url_shortener,
		       access_count, ip_address, created_at, updated_at
		FROM metadata
		WHERE resource_path IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var metadata model.FileMetadata
		var expiresAt sql.NullTime
		err := rows.Scan(
			&metadata.ResourcePath,
			&metadata.Token,
			&metadata.OriginalName,
			&metadata.UploadDate,
			&expiresAt,
			&metadata.Size,
			&metadata.ContentType,
			&metadata.OneTimeView,
			&metadata.OriginalURL,
			&metadata.IsURLShortener,
			&metadata.AccessCount,
			&metadata.IPAddress,
			&metadata.CreatedAt,
			&metadata.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		// Handle NULL expires_at
		if expiresAt.Valid {
			metadata.ExpiresAt = &expiresAt.Time
		}

		metadataList = append(metadataList, metadata)
	}

	return metadataList, rows.Err()
}

// DeleteMetadata deletes metadata
func (db *DB) DeleteMetadata(meta Storeable) error {
	stmt, err := db.Prepare("DELETE FROM metadata WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(meta.ID())
	return err
}

// ListMetadataFilteredAndSorted returns metadata with optional filtering and sorting
func (db *DB) ListMetadataFilteredAndSorted(searchQuery, sortField, sortDirection string) ([]model.FileMetadata, error) {
	var query string
	var args []interface{}

	// Build WHERE clause for search
	whereClause := ""
	if searchQuery != "" {
		whereClause = "WHERE (LOWER(REPLACE(resource_path, 'uploads/', '')) LIKE ? OR LOWER(original_name) LIKE ?)"
		searchPattern := "%" + strings.ToLower(searchQuery) + "%"
		args = append(args, searchPattern, searchPattern)
	}

	// Build ORDER BY clause
	orderBy := "ORDER BY "
	switch sortField {
	case "filename":
		orderBy += "resource_path"
	case "originalName":
		orderBy += "original_name"
	case "size":
		orderBy += "size"
	case "uploadDate":
		orderBy += "upload_date"
	case "expires":
		// For expires, we need to handle expired files specially
		orderBy += "CASE WHEN expires_at IS NULL OR expires_at > datetime('now') THEN 0 ELSE 1 END, expires_at"
	default:
		orderBy += "upload_date"
	}

	if sortDirection == "asc" {
		orderBy += " ASC"
	} else {
		orderBy += " DESC"
	}

	// Build the complete query
	query = fmt.Sprintf(`
		SELECT resource_path, token, original_name, upload_date, expires_at, 
		       size, content_type, one_time_view, original_url, is_url_shortener,
		       access_count, ip_address, created_at, updated_at
		FROM metadata 
		%s 
		%s
	`, whereClause, orderBy)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metadataList []model.FileMetadata
	for rows.Next() {
		var metadata model.FileMetadata
		var expiresAt sql.NullTime
		err := rows.Scan(
			&metadata.ResourcePath,
			&metadata.Token,
			&metadata.OriginalName,
			&metadata.UploadDate,
			&expiresAt,
			&metadata.Size,
			&metadata.ContentType,
			&metadata.OneTimeView,
			&metadata.OriginalURL,
			&metadata.IsURLShortener,
			&metadata.AccessCount,
			&metadata.IPAddress,
			&metadata.CreatedAt,
			&metadata.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		// Handle NULL expires_at
		if expiresAt.Valid {
			metadata.ExpiresAt = &expiresAt.Time
		}

		metadataList = append(metadataList, metadata)
	}

	return metadataList, rows.Err()
}

// CountMetadataFiltered returns count of metadata matching search criteria
func (db *DB) CountMetadataFiltered(searchQuery string) (int, error) {
	var count int

	if searchQuery != "" {
		searchPattern := "%" + strings.ToLower(searchQuery) + "%"
		err := db.Get(&count, "SELECT COUNT(*) FROM metadata WHERE (LOWER(REPLACE(resource_path, 'uploads/', '')) LIKE ? OR LOWER(original_name) LIKE ?)", searchPattern, searchPattern)
		return count, err
	} else {
		err := db.Get(&count, "SELECT COUNT(*) FROM metadata")
		return count, err
	}
}

// GetTotalSize returns the total size of all files in bytes
func (db *DB) GetTotalSize() (int64, error) {
	var totalSize int64
	err := db.Get(&totalSize, "SELECT COALESCE(SUM(size), 0) FROM metadata")
	return totalSize, err
}

// ListMetadataFilteredAndSortedWithPagination returns metadata with pagination using cursor
func (db *DB) ListMetadataFilteredAndSortedWithPagination(searchQuery, sortField, sortDirection string, limit int, cursor string) ([]model.FileMetadata, string, error) {
	var query string
	var args []interface{}

	// Build WHERE clause for search
	whereClause := ""
	if searchQuery != "" {
		whereClause = "WHERE (LOWER(REPLACE(resource_path, 'uploads/', '')) LIKE ? OR LOWER(original_name) LIKE ?)"
		searchPattern := "%" + strings.ToLower(searchQuery) + "%"
		args = append(args, searchPattern, searchPattern)
	}

	// Build ORDER BY clause
	orderBy := "ORDER BY "
	cursorCondition := ""
	switch sortField {
	case "filename":
		orderBy += "resource_path"
		if cursor != "" {
			if sortDirection == "asc" {
				cursorCondition = " AND resource_path > ?"
			} else {
				cursorCondition = " AND resource_path < ?"
			}
		}
	case "originalName":
		orderBy += "original_name"
		if cursor != "" {
			if sortDirection == "asc" {
				cursorCondition = " AND original_name > ?"
			} else {
				cursorCondition = " AND original_name < ?"
			}
		}
	case "size":
		orderBy += "size"
		if cursor != "" {
			if sortDirection == "asc" {
				cursorCondition = " AND size > ?"
			} else {
				cursorCondition = " AND size < ?"
			}
		}
	case "uploadDate":
		orderBy += "upload_date"
		if cursor != "" {
			if sortDirection == "asc" {
				cursorCondition = " AND upload_date > ?"
			} else {
				cursorCondition = " AND upload_date < ?"
			}
		}
	case "expires":
		// Sort by expiration date, with NULL values (no expiration) at the end
		orderBy += "expires_at"
		if cursor != "" {
			if sortDirection == "asc" {
				cursorCondition = " AND expires_at > ?"
			} else {
				cursorCondition = " AND expires_at < ?"
			}
		}
	default:
		orderBy += "upload_date"
		if cursor != "" {
			if sortDirection == "asc" {
				cursorCondition = " AND upload_date > ?"
			} else {
				cursorCondition = " AND upload_date < ?"
			}
		}
	}

	if sortDirection == "asc" {
		orderBy += " ASC"
	} else {
		orderBy += " DESC"
	}

	// Add cursor condition to WHERE clause
	if cursorCondition != "" {
		if whereClause == "" {
			whereClause = "WHERE 1=1" + cursorCondition
		} else {
			whereClause += cursorCondition
		}
		if cursor != "" {
			args = append(args, cursor)
		}
	}

	// Add LIMIT
	limitClause := fmt.Sprintf(" LIMIT %d", limit+1) // Get one extra to check if there are more

	// Build the complete query
	query = fmt.Sprintf(`
		SELECT resource_path, token, original_name, upload_date, expires_at, 
		       size, content_type, one_time_view, original_url, is_url_shortener,
		       access_count, ip_address, created_at, updated_at
		FROM metadata 
		%s 
		%s
		%s
	`, whereClause, orderBy, limitClause)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var metadataList []model.FileMetadata
	var nextCursor string

	for rows.Next() {
		var metadata model.FileMetadata
		var expiresAt sql.NullTime
		err := rows.Scan(
			&metadata.ResourcePath,
			&metadata.Token,
			&metadata.OriginalName,
			&metadata.UploadDate,
			&expiresAt,
			&metadata.Size,
			&metadata.ContentType,
			&metadata.OneTimeView,
			&metadata.OriginalURL,
			&metadata.IsURLShortener,
			&metadata.AccessCount,
			&metadata.IPAddress,
			&metadata.CreatedAt,
			&metadata.UpdatedAt,
		)
		if err != nil {
			return nil, "", err
		}

		// Handle NULL expires_at
		if expiresAt.Valid {
			metadata.ExpiresAt = &expiresAt.Time
		}

		metadataList = append(metadataList, metadata)

		// If we have more than the limit, set the next cursor
		if len(metadataList) == limit {
			// Determine cursor value based on sort field
			switch sortField {
			case "filename":
				nextCursor = metadata.ResourcePath
			case "originalName":
				nextCursor = metadata.OriginalName
			case "size":
				nextCursor = fmt.Sprintf("%d", metadata.Size)
			case "uploadDate":
				nextCursor = metadata.UploadDate.Format("2006-01-02T15:04:05Z07:00")
			case "expires":
				if metadata.ExpiresAt != nil {
					nextCursor = metadata.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
				} else {
					nextCursor = metadata.UploadDate.Format("2006-01-02T15:04:05Z07:00")
				}
			default:
				nextCursor = metadata.UploadDate.Format("2006-01-02T15:04:05Z07:00")
			}
			break
		}
	}

	// Remove the extra record if we got it
	if len(metadataList) > limit {
		metadataList = metadataList[:limit]
	}

	return metadataList, nextCursor, rows.Err()
}
