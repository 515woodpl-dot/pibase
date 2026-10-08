package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/jackc/pgx/v5"
)

const maxStorageObjectBytes = 1 << 30

type bucketRequest struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Public           bool     `json:"public"`
	FileSizeLimit    *int64   `json:"file_size_limit"`
	AllowedMimeTypes []string `json:"allowed_mime_types"`
}

type objectInfo struct {
	Bucket string
	Name   string
	Size   int64
	Mime   string
}

func storagePrincipal(s *Server, r *http.Request, write bool) (principal, error) {
	who, err := s.authenticate(r.Context(), r)
	if err != nil {
		return principal{}, err
	}
	if write && who.role == who.project.anonRole {
		return principal{}, httpError(http.StatusUnauthorized, "authenticated storage session required")
	}
	if err := s.enforceRate(who); err != nil {
		return principal{}, err
	}
	s.recordRequest(r.Context(), who.project.id)
	return who, nil
}

func storageServicePrincipal(p project) principal {
	return principal{project: &p, role: p.serviceRole, claims: []byte(`{"role":"service_role"}`)}
}

func (s *Server) storageTransaction(ctx context.Context, who principal, fn func(pgx.Tx) error) error {
	select {
	case s.databaseSlots <- struct{}{}:
		defer func() { <-s.databaseSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	pool, err := s.projectPool(ctx, who.project)
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{who.role}.Sanitize()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('request.jwt.claims',$1,true), set_config('statement_timeout','5s',true)`, string(who.claims)); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func storageBucketName(raw string) (string, error) {
	name, err := url.PathUnescape(raw)
	if err != nil || name == "" || name == "." || name == ".." || len(name) > 100 || strings.ContainsAny(name, "/\\\x00") {
		return "", httpError(http.StatusBadRequest, "invalid bucket name")
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return "", httpError(http.StatusBadRequest, "invalid bucket name")
		}
	}
	return name, nil
}

func storageObjectName(raw string) (string, error) {
	name, err := url.PathUnescape(raw)
	if err != nil || name == "" || len(name) > 1024 || strings.ContainsRune(name, '\x00') || strings.ContainsRune(name, '\\') || strings.HasPrefix(name, "/") {
		return "", httpError(http.StatusBadRequest, "invalid object path")
	}
	clean := filepath.Clean(name)
	if clean != name || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", httpError(http.StatusBadRequest, "invalid object path")
	}
	return name, nil
}

func (s *Server) storageFile(p *project, bucket, name string) (string, error) {
	// Validate catalog values too: older databases may contain unsafe names.
	if validated, err := storageBucketName(bucket); err != nil || validated != bucket {
		return "", httpError(http.StatusBadRequest, "invalid bucket name")
	}
	if validated, err := storageObjectName(url.PathEscape(name)); err != nil || validated != name {
		return "", httpError(http.StatusBadRequest, "invalid object path")
	}
	return filepath.Join(s.storageRoot, p.id, bucket, filepath.FromSlash(name)), nil
}

// Read from the network outside the lock. Serialize only quota accounting and
// local writes, so stalled clients cannot block deletes or other uploads.
type storageQuotaWriter struct {
	s         *Server
	ctx       context.Context
	projectID string
	file      *os.File
}

func (w storageQuotaWriter) Write(data []byte) (int, error) {
	release, err := w.s.acquireStorageLock(w.ctx, w.projectID)
	if err != nil {
		return 0, err
	}
	defer release()
	usage, err := storageUsage(w.s.storageRoot, w.projectID)
	if err != nil {
		return 0, err
	}
	if int64(len(data)) > w.s.storageQuotaBytes-usage {
		return 0, httpError(http.StatusInsufficientStorage, "storage quota exceeded")
	}
	return w.file.Write(data)
}

var errStoragePreflightComplete = errors.New("storage authorization checked")

func (s *Server) storageUploadPreflight(ctx context.Context, who principal, bucket, name, owner string, upsert bool) error {
	err := s.storageTransaction(ctx, who, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM storage.objects WHERE bucket_id=$1 AND name=$2`, bucket, name).Scan(&id)
		if err == nil {
			if !upsert {
				return httpError(http.StatusConflict, "object already exists")
			}
			tag, err := tx.Exec(ctx, `UPDATE storage.objects SET name=name WHERE id=$1`, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return httpError(http.StatusForbidden, "object update is not allowed")
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			id, err = auth.UUID()
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO storage.objects(id,bucket_id,name,owner_id,size) VALUES($1,$2,$3,$4,0)`, id, bucket, name, nullableUUID(owner)); err != nil {
				return err
			}
		} else {
			return err
		}
		// Roll back the probe; authorization is checked again at finalization.
		return errStoragePreflightComplete
	})
	if errors.Is(err, errStoragePreflightComplete) {
		return nil
	}
	return err
}

func (s *Server) acquireStorageLock(ctx context.Context, projectID string) (func(), error) {
	value, _ := s.storageUploadLocks.LoadOrStore(projectID, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func storageSubject(who principal) string {
	var claims struct {
		Subject string `json:"sub"`
	}
	_ = json.Unmarshal(who.claims, &claims)
	return claims.Subject
}

func decodeStorageJSON(r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return bodyError(err, "invalid JSON body")
	}
	return nil
}

func (s *Server) storageListBuckets(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var buckets []map[string]any
	err = s.storageTransaction(r.Context(), storageServicePrincipal(p), func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `SELECT id,name,public,file_size_limit,allowed_mime_types,created_at,updated_at FROM storage.buckets ORDER BY name`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			var public bool
			var limit *int64
			var mime []string
			var created, updated time.Time
			if e = rows.Scan(&id, &name, &public, &limit, &mime, &created, &updated); e != nil {
				return e
			}
			buckets = append(buckets, bucketJSON(id, name, public, limit, mime, created, updated))
		}
		return rows.Err()
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buckets)
}

func bucketJSON(id, name string, public bool, limit *int64, mime []string, created, updated time.Time) map[string]any {
	return map[string]any{"id": id, "name": name, "public": public, "file_size_limit": limit, "allowed_mime_types": mime, "created_at": created.UTC().Format(time.RFC3339Nano), "updated_at": updated.UTC().Format(time.RFC3339Nano)}
}

func (s *Server) storageCreateBucket(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req bucketRequest
	if err = decodeStorageJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	name := req.Name
	if name == "" {
		name = req.ID
	}
	name, err = storageBucketName(name)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if req.FileSizeLimit != nil && *req.FileSizeLimit <= 0 {
		writeAPIError(w, httpError(400, "file_size_limit must be positive"))
		return
	}
	err = s.storageTransaction(r.Context(), storageServicePrincipal(p), func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `INSERT INTO storage.buckets(id,name,public,file_size_limit,allowed_mime_types) VALUES($1,$1,$2,$3,$4)`, name, req.Public, req.FileSizeLimit, req.AllowedMimeTypes)
		return e
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			err = httpError(http.StatusConflict, "bucket already exists")
		}
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": name, "id": name, "public": req.Public})
}

func (s *Server) storageGetBucket(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	name, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var out map[string]any
	err = s.storageTransaction(r.Context(), storageServicePrincipal(p), func(tx pgx.Tx) error {
		var id, bucketName string
		var public bool
		var limit *int64
		var mime []string
		var created, updated time.Time
		if e := tx.QueryRow(r.Context(), `SELECT id,name,public,file_size_limit,allowed_mime_types,created_at,updated_at FROM storage.buckets WHERE id=$1`, name).Scan(&id, &bucketName, &public, &limit, &mime, &created, &updated); e != nil {
			return e
		}
		out = bucketJSON(id, bucketName, public, limit, mime, created, updated)
		return nil
	})
	if err != nil {
		writeAPIError(w, httpError(http.StatusNotFound, "bucket not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) storageUpdateBucket(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	name, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req bucketRequest
	if err = decodeStorageJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	err = s.storageTransaction(r.Context(), storageServicePrincipal(p), func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `UPDATE storage.buckets SET public=COALESCE($1,public),file_size_limit=COALESCE($2,file_size_limit),allowed_mime_types=COALESCE($3,allowed_mime_types),updated_at=now() WHERE id=$4`, req.Public, req.FileSizeLimit, req.AllowedMimeTypes, name)
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": name, "name": name})
}

func (s *Server) storageDeleteBucket(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	name, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	release, err := s.acquireStorageLock(r.Context(), p.id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer release()
	err = s.storageTransaction(r.Context(), storageServicePrincipal(p), func(tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(r.Context(), `SELECT count(*) FROM storage.objects WHERE bucket_id=$1`, name).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return httpError(http.StatusConflict, "bucket is not empty")
		}
		_, e := tx.Exec(r.Context(), `DELETE FROM storage.buckets WHERE id=$1`, name)
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if removeErr := os.RemoveAll(filepath.Join(s.storageRoot, p.id, name)); removeErr != nil {
		writeAPIError(w, removeErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Successfully deleted"})
}

func (s *Server) storageUploadObject(w http.ResponseWriter, r *http.Request) {
	who, err := storagePrincipal(s, r, true)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	bucket, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	name, err := storageObjectName(r.PathValue("path"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	mimeType := r.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	cacheControl := r.Header.Get("Cache-Control")
	upsert := strings.EqualFold(r.Header.Get("x-upsert"), "true")
	owner := storageSubject(who)
	if owner == "" && who.role != who.project.serviceRole {
		writeAPIError(w, httpError(http.StatusUnauthorized, "authenticated storage session required"))
		return
	}
	value, _ := s.storageUploadSlots.LoadOrStore(who.project.id, make(chan struct{}, 8))
	slots := value.(chan struct{})
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		writeAPIError(w, httpError(http.StatusTooManyRequests, "too many active uploads"))
		return
	}
	finalPath, err := s.storageFile(who.project, bucket, name)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	tmpPath := finalPath + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	backupPath := tmpPath + ".old"
	defer func() { _ = os.Remove(tmpPath) }()
	var bucketLimit *int64
	var allowedMimes []string
	if err = s.storageTransaction(r.Context(), storageServicePrincipal(*who.project), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT file_size_limit,allowed_mime_types FROM storage.buckets WHERE id=$1`, bucket).Scan(&bucketLimit, &allowedMimes)
	}); err != nil {
		writeAPIError(w, httpError(http.StatusNotFound, "bucket not found"))
		return
	}
	if err = s.storageUploadPreflight(r.Context(), who, bucket, name, owner, upsert); err != nil {
		writeAPIError(w, err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		writeAPIError(w, err)
		return
	}
	copyLimit := int64(maxStorageObjectBytes)
	tmp, openErr := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if openErr != nil {
		writeAPIError(w, openErr)
		return
	}
	quotaWriter := storageQuotaWriter{s: s, ctx: r.Context(), projectID: who.project.id, file: tmp}
	var size int64
	if strings.HasPrefix(strings.ToLower(mimeType), "multipart/form-data") {
		mediaType, params, parseErr := mime.ParseMediaType(mimeType)
		if parseErr != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			_ = tmp.Close()
			writeAPIError(w, httpError(http.StatusBadRequest, "invalid multipart body"))
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		foundFile := false
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				_ = tmp.Close()
				writeAPIError(w, httpError(http.StatusBadRequest, "invalid multipart body"))
				return
			}
			if part.FileName() != "" || part.FormName() == "" {
				if foundFile {
					continue
				}
				foundFile = true
				if part.Header.Get("Content-Type") != "" {
					mimeType = part.Header.Get("Content-Type")
				}
				size, err = copyLimited(quotaWriter, part, copyLimit)
				if err != nil {
					_ = tmp.Close()
					if errors.Is(err, errStorageObjectTooLarge) {
						writeAPIError(w, storageSizeError(copyLimit))
					} else {
						writeAPIError(w, err)
					}
					return
				}
			} else if part.FormName() == "cacheControl" {
				value, readErr := io.ReadAll(io.LimitReader(part, 4096))
				if readErr != nil {
					_ = tmp.Close()
					writeAPIError(w, readErr)
					return
				}
				cacheControl = string(value)
			}
		}
		if !foundFile {
			_ = tmp.Close()
			writeAPIError(w, httpError(http.StatusBadRequest, "multipart file is required"))
			return
		}
	} else {
		size, err = copyLimited(quotaWriter, r.Body, copyLimit)
		if err != nil {
			_ = tmp.Close()
			if errors.Is(err, errStorageObjectTooLarge) {
				writeAPIError(w, storageSizeError(copyLimit))
			} else {
				writeAPIError(w, err)
			}
			return
		}
	}
	if err = tmp.Close(); err != nil {
		writeAPIError(w, err)
		return
	}
	release, err := s.acquireStorageLock(r.Context(), who.project.id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer release()
	var objectID string
	var swapped, hadPreviousFile bool
	err = s.storageTransaction(r.Context(), who, func(tx pgx.Tx) error {
		if bucketLimit != nil && size > *bucketLimit {
			return httpError(http.StatusRequestEntityTooLarge, "object exceeds bucket file size limit")
		}
		if len(allowedMimes) > 0 {
			matched := false
			for _, pattern := range allowedMimes {
				if pattern == mimeType || (strings.HasSuffix(pattern, "/*") && strings.HasPrefix(mimeType, strings.TrimSuffix(pattern, "*"))) {
					matched = true
				}
			}
			if !matched {
				return httpError(http.StatusUnsupportedMediaType, "mime type is not allowed for bucket")
			}
		}
		err := tx.QueryRow(r.Context(), `SELECT id FROM storage.objects WHERE bucket_id=$1 AND name=$2`, bucket, name).Scan(&objectID)
		if err == nil && !upsert {
			return httpError(http.StatusConflict, "object already exists")
		}
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if objectID == "" {
			objectID, err = auth.UUID()
			if err != nil {
				return err
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO storage.objects(id,bucket_id,name,owner_id,metadata,size,mime_type) VALUES($1,$2,$3,$4,$5,$6,$7)`, objectID, bucket, name, nullableUUID(owner), map[string]any{"mimetype": mimeType, "cacheControl": cacheControl}, size, mimeType)
		} else {
			tag, updateErr := tx.Exec(r.Context(), `UPDATE storage.objects SET metadata=$1,size=$2,mime_type=$3,updated_at=now() WHERE id=$4`, map[string]any{"mimetype": mimeType, "cacheControl": cacheControl}, size, mimeType, objectID)
			if updateErr != nil {
				return updateErr
			}
			if tag.RowsAffected() != 1 {
				return httpError(http.StatusForbidden, "object update is not allowed")
			}
		}
		if err != nil {
			return err
		}
		if existing, statErr := os.Lstat(finalPath); statErr == nil {
			if !existing.Mode().IsRegular() {
				return httpError(http.StatusConflict, "object path conflicts with an existing directory or link")
			}
			if err = os.Rename(finalPath, backupPath); err != nil {
				return err
			}
			hadPreviousFile = true
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		if err = os.Rename(tmpPath, finalPath); err != nil {
			if hadPreviousFile {
				_ = os.Rename(backupPath, finalPath)
			}
			return err
		}
		swapped = true
		return nil
	})
	if err != nil {
		if swapped {
			_ = os.Remove(finalPath)
			if hadPreviousFile {
				_ = os.Rename(backupPath, finalPath)
			}
		}
		writeAPIError(w, err)
		return
	}
	if hadPreviousFile {
		_ = os.Remove(backupPath)
	}
	writeJSON(w, http.StatusOK, map[string]any{"Key": bucket + "/" + name, "Id": objectID, "FullPath": bucket + "/" + name})
}

var errStorageObjectTooLarge = errors.New("storage object too large")

func storageSizeError(limit int64) error {
	if limit < maxStorageObjectBytes {
		return httpError(http.StatusInsufficientStorage, "storage quota exceeded")
	}
	return httpError(http.StatusRequestEntityTooLarge, "object exceeds 1 GiB limit")
}

func copyLimited(dst io.Writer, src io.Reader, maximum int64) (int64, error) {
	limited := io.LimitReader(src, maximum+1)
	n, err := io.Copy(dst, limited)
	if err != nil {
		return n, err
	}
	if n > maximum {
		return n, errStorageObjectTooLarge
	}
	return n, nil
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func storageUsage(root, project string) (int64, error) {
	var total int64
	err := filepath.Walk(filepath.Join(root, project), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func (s *Server) readStorageObject(ctx context.Context, who principal, bucket, name string) (objectInfo, error) {
	var object objectInfo
	err := s.storageTransaction(ctx, who, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT bucket_id,name,size,COALESCE(mime_type,'application/octet-stream') FROM storage.objects WHERE bucket_id=$1 AND name=$2`, bucket, name).Scan(&object.Bucket, &object.Name, &object.Size, &object.Mime)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return objectInfo{}, httpError(http.StatusNotFound, "object not found")
		}
		return objectInfo{}, err
	}
	return object, nil
}

func (s *Server) serveStorageObject(w http.ResponseWriter, r *http.Request, p *project, object objectInfo) {
	file, err := s.storageFile(p, object.Bucket, object.Name)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if _, err := os.Stat(file); err != nil {
		writeAPIError(w, httpError(http.StatusNotFound, "object data not found"))
		return
	}
	w.Header().Set("Content-Type", object.Mime)
	w.Header().Set("Content-Length", strconv.FormatInt(object.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filepath.Base(object.Name)))
	http.ServeFile(w, r, file)
}

func (s *Server) storageDownloadObject(w http.ResponseWriter, r *http.Request) {
	who, err := storagePrincipal(s, r, false)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	bucket, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	name, err := storageObjectName(r.PathValue("path"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	object, err := s.readStorageObject(r.Context(), who, bucket, name)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	s.serveStorageObject(w, r, who.project, object)
}

func (s *Server) storagePublicObject(w http.ResponseWriter, r *http.Request) {
	bucket, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	name, err := storageObjectName(r.PathValue("path"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var p project
	var object objectInfo
	if strings.TrimSpace(r.Header.Get("apikey")) != "" {
		who, authErr := storagePrincipal(s, r, false)
		if authErr != nil {
			writeAPIError(w, authErr)
			return
		}
		p = *who.project
		object, err = s.readStorageObject(r.Context(), who, bucket, name)
	} else {
		p, object, err = s.findPublicObject(r.Context(), bucket, name)
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	s.serveStorageObject(w, r, &p, object)
}

func (s *Server) findPublicObject(ctx context.Context, bucket, name string) (project, objectInfo, error) {
	rows, err := s.control.Query(ctx, `SELECT id FROM control.project_catalog ORDER BY id`)
	if err != nil {
		return project{}, objectInfo{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return project{}, objectInfo{}, err
		}
		p, err := s.resolveProjectID(ctx, id)
		if err != nil {
			continue
		}
		var object objectInfo
		err = s.storageTransaction(ctx, storageServicePrincipal(p), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT o.bucket_id,o.name,o.size,COALESCE(o.mime_type,'application/octet-stream') FROM storage.objects o JOIN storage.buckets b ON b.id=o.bucket_id WHERE b.id=$1 AND b.public AND o.name=$2`, bucket, name).Scan(&object.Bucket, &object.Name, &object.Size, &object.Mime)
		})
		if err == nil {
			return p, object, nil
		}
	}
	if err := rows.Err(); err != nil {
		return project{}, objectInfo{}, err
	}
	return project{}, objectInfo{}, httpError(http.StatusNotFound, "object not found")
}

func (s *Server) storageListObjects(w http.ResponseWriter, r *http.Request) {
	who, err := storagePrincipal(s, r, false)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	bucket, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req struct {
		Prefix string `json:"prefix"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
		Search string `json:"search"`
	}
	if err = decodeStorageJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		req.Limit = 100
	}
	if req.Offset < 0 {
		req.Offset = 0
	}
	var objects []map[string]any
	err = s.storageTransaction(r.Context(), who, func(tx pgx.Tx) error {
		pattern := req.Prefix + "%"
		rows, e := tx.Query(r.Context(), `SELECT name,id,metadata,size,mime_type,created_at,updated_at FROM storage.objects WHERE bucket_id=$1 AND name LIKE $2 AND ($3='' OR name ILIKE '%'||$3||'%') ORDER BY name LIMIT $4 OFFSET $5`, bucket, pattern, req.Search, req.Limit, req.Offset)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var name, id string
			var metadata map[string]any
			var size int64
			var mime *string
			var created, updated time.Time
			if e = rows.Scan(&name, &id, &metadata, &size, &mime, &created, &updated); e != nil {
				return e
			}
			objects = append(objects, map[string]any{"name": name, "id": id, "metadata": metadata, "size": size, "mimetype": mime, "created_at": created.UTC().Format(time.RFC3339Nano), "updated_at": updated.UTC().Format(time.RFC3339Nano)})
		}
		return rows.Err()
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, objects)
}

func (s *Server) storageDeleteObjects(w http.ResponseWriter, r *http.Request) {
	who, err := storagePrincipal(s, r, true)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	bucket, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var request struct {
		Prefixes []json.RawMessage `json:"prefixes"`
	}
	if err = decodeStorageJSON(r, &request); err != nil || len(request.Prefixes) == 0 || len(request.Prefixes) > 100 {
		writeAPIError(w, httpError(http.StatusBadRequest, "object path list is invalid"))
		return
	}
	names := make([]string, 0, len(request.Prefixes))
	seen := make(map[string]bool, len(request.Prefixes))
	for _, raw := range request.Prefixes {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			var entry struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(raw, &entry) != nil {
				writeAPIError(w, httpError(http.StatusBadRequest, "object path list is invalid"))
				return
			}
			name = entry.Path
		}
		if name, err = storageObjectName(name); err != nil {
			writeAPIError(w, err)
			return
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	release, err := s.acquireStorageLock(r.Context(), who.project.id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer release()
	err = s.storageTransaction(r.Context(), who, func(tx pgx.Tx) error {
		for _, name := range names {
			tag, e := tx.Exec(r.Context(), `DELETE FROM storage.objects WHERE bucket_id=$1 AND name=$2`, bucket, name)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return httpError(http.StatusNotFound, "object not found")
			}
		}
		return nil
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	for _, name := range names {
		file, pathErr := s.storageFile(who.project, bucket, name)
		if pathErr != nil {
			writeAPIError(w, pathErr)
			return
		}
		if removeErr := os.Remove(file); removeErr != nil && !os.IsNotExist(removeErr) {
			writeAPIError(w, removeErr)
			return
		}
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) storageSignObject(w http.ResponseWriter, r *http.Request) {
	bucket, err := storageBucketName(r.PathValue("bucket"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	name, err := storageObjectName(r.PathValue("path"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		token := r.URL.Query().Get("token")
		var tokenMeta struct {
			ProjectID string `json:"project_id"`
		}
		parts := strings.Split(token, ".")
		if len(parts) == 3 {
			if payload, decodeErr := base64.RawURLEncoding.DecodeString(parts[1]); decodeErr == nil {
				_ = json.Unmarshal(payload, &tokenMeta)
			}
		}
		var p project
		var resolveErr error
		if strings.TrimSpace(r.Header.Get("apikey")) != "" {
			p, resolveErr = s.resolveAuthProject(r)
		} else if tokenMeta.ProjectID != "" {
			p, resolveErr = s.resolveProjectID(r.Context(), tokenMeta.ProjectID)
		} else {
			resolveErr = httpError(http.StatusUnauthorized, "invalid signed URL")
		}
		if resolveErr != nil {
			writeAPIError(w, resolveErr)
			return
		}
		claims, verifyErr := keys.VerifyJWT(token, p.secret, time.Now())
		if verifyErr != nil {
			writeAPIError(w, httpError(http.StatusUnauthorized, "invalid signed URL"))
			return
		}
		var signed struct{ Bucket, Path string }
		if json.Unmarshal(claims.JSON, &signed) != nil || signed.Bucket != bucket || signed.Path != name {
			writeAPIError(w, httpError(http.StatusUnauthorized, "invalid signed URL"))
			return
		}
		object, readErr := s.readStorageObject(r.Context(), storageServicePrincipal(p), bucket, name)
		if readErr != nil {
			writeAPIError(w, readErr)
			return
		}
		s.serveStorageObject(w, r, &p, object)
		return
	}
	who, err := storagePrincipal(s, r, false)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req struct {
		ExpiresIn int `json:"expiresIn"`
	}
	if err = decodeStorageJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.ExpiresIn <= 0 || req.ExpiresIn > 86400 {
		req.ExpiresIn = 3600
	}
	if _, err = s.readStorageObject(r.Context(), who, bucket, name); err != nil {
		writeAPIError(w, err)
		return
	}
	token, err := keys.SignJWT(who.project.secret, map[string]any{"iss": "pibase", "role": "storage", "project_id": who.project.id, "bucket": bucket, "path": name, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Duration(req.ExpiresIn) * time.Second).Unix()})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signedURL": "/storage/v1/object/sign/" + bucket + "/" + name + "?token=" + url.QueryEscape(token)})
}
