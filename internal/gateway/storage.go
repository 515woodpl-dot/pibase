package gateway

import (
	"bytes"
	"context"
	"encoding/json"
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

const maxStorageObjectBytes = 1 << 20

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
	if err != nil || name == "" || len(name) > 100 || strings.ContainsAny(name, `/\\\x00`) {
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

func (s *Server) storageFile(p *project, bucket, name string) string {
	return filepath.Join(s.storageRoot, p.id, bucket, filepath.FromSlash(name))
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
		return httpError(http.StatusBadRequest, "invalid JSON body")
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
	_ = os.RemoveAll(filepath.Join(s.storageRoot, p.id, name))
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
	data, err := io.ReadAll(io.LimitReader(r.Body, maxStorageObjectBytes+1))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if len(data) > maxStorageObjectBytes {
		writeAPIError(w, httpError(http.StatusRequestEntityTooLarge, "object exceeds 1 MiB request limit"))
		return
	}
	mimeType := r.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	cacheControl := r.Header.Get("Cache-Control")
	if strings.HasPrefix(strings.ToLower(mimeType), "multipart/form-data") {
		mediaType, params, parseErr := mime.ParseMediaType(mimeType)
		if parseErr != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			writeAPIError(w, httpError(http.StatusBadRequest, "invalid multipart body"))
			return
		}
		reader := multipart.NewReader(bytes.NewReader(data), params["boundary"])
		var fileData []byte
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				writeAPIError(w, httpError(http.StatusBadRequest, "invalid multipart body"))
				return
			}
			partData, readErr := io.ReadAll(part)
			if readErr != nil {
				writeAPIError(w, readErr)
				return
			}
			if part.FileName() != "" || part.FormName() == "" {
				fileData = partData
				if part.Header.Get("Content-Type") != "" {
					mimeType = part.Header.Get("Content-Type")
				}
			} else if part.FormName() == "cacheControl" {
				cacheControl = string(partData)
			}
		}
		data = fileData
	}
	upsert := strings.EqualFold(r.Header.Get("x-upsert"), "true")
	owner := storageSubject(who)
	if owner == "" && who.role != who.project.serviceRole {
		writeAPIError(w, httpError(http.StatusUnauthorized, "authenticated storage session required"))
		return
	}
	finalPath := s.storageFile(who.project, bucket, name)
	tmpPath := finalPath + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		writeAPIError(w, err)
		return
	}
	if err = os.WriteFile(tmpPath, data, 0o640); err != nil {
		writeAPIError(w, err)
		return
	}
	var objectID string
	err = s.storageTransaction(r.Context(), who, func(tx pgx.Tx) error {
		var limit *int64
		var allowed []string
		if e := tx.QueryRow(r.Context(), `SELECT public,file_size_limit,allowed_mime_types FROM storage.buckets WHERE id=$1`, bucket).Scan(new(bool), &limit, &allowed); e != nil {
			return httpError(http.StatusNotFound, "bucket not found or inaccessible")
		}
		if limit != nil && int64(len(data)) > *limit {
			return httpError(http.StatusRequestEntityTooLarge, "object exceeds bucket file size limit")
		}
		if len(allowed) > 0 {
			matched := false
			for _, pattern := range allowed {
				if pattern == mimeType || (strings.HasSuffix(pattern, "/*") && strings.HasPrefix(mimeType, strings.TrimSuffix(pattern, "*"))) {
					matched = true
				}
			}
			if !matched {
				return httpError(http.StatusUnsupportedMediaType, "mime type is not allowed for bucket")
			}
		}
		var previousSize int64
		err := tx.QueryRow(r.Context(), `SELECT id,size FROM storage.objects WHERE bucket_id=$1 AND name=$2`, bucket, name).Scan(&objectID, &previousSize)
		if err == nil && !upsert {
			return httpError(http.StatusConflict, "object already exists")
		}
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		usage, usageErr := storageUsage(s.storageRoot, who.project.id)
		if usageErr != nil {
			return usageErr
		}
		if usage-int64(previousSize)+int64(len(data)) > s.storageQuotaBytes {
			return httpError(http.StatusInsufficientStorage, "storage quota exceeded")
		}
		if objectID == "" {
			objectID, err = auth.UUID()
			if err != nil {
				return err
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO storage.objects(id,bucket_id,name,owner_id,metadata,size,mime_type) VALUES($1,$2,$3,$4,$5,$6,$7)`, objectID, bucket, name, nullableUUID(owner), map[string]any{"mimetype": mimeType, "cacheControl": cacheControl}, len(data), mimeType)
		} else {
			_, err = tx.Exec(r.Context(), `UPDATE storage.objects SET metadata=$1,size=$2,mime_type=$3,updated_at=now() WHERE id=$4`, map[string]any{"mimetype": mimeType, "cacheControl": cacheControl}, len(data), mimeType, objectID)
		}
		return err
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err = os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(finalPath)
		writeAPIError(w, err)
		return
	}
	committed = true
	writeJSON(w, http.StatusOK, map[string]any{"Key": bucket + "/" + name, "Id": objectID, "FullPath": bucket + "/" + name})
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
		if !info.IsDir() && !strings.Contains(filepath.Base(path), ".tmp-") {
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
	file := s.storageFile(p, object.Bucket, object.Name)
	if _, err := os.Stat(file); err != nil {
		writeAPIError(w, httpError(http.StatusNotFound, "object data not found"))
		return
	}
	w.Header().Set("Content-Type", object.Mime)
	w.Header().Set("Content-Length", strconv.FormatInt(object.Size, 10))
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
	s.storageDownloadObject(w, r)
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
		names = append(names, name)
	}
	err = s.storageTransaction(r.Context(), who, func(tx pgx.Tx) error {
		for _, name := range names {
			if _, e := tx.Exec(r.Context(), `DELETE FROM storage.objects WHERE bucket_id=$1 AND name=$2`, bucket, name); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	for _, name := range names {
		_ = os.Remove(s.storageFile(who.project, bucket, name))
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
		p, err := s.resolveAuthProject(r)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		token := r.URL.Query().Get("token")
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
	token, err := keys.SignJWT(who.project.secret, map[string]any{"iss": "pibase", "role": "storage", "bucket": bucket, "path": name, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Duration(req.ExpiresIn) * time.Second).Unix()})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signedURL": "/storage/v1/object/sign/" + bucket + "/" + name + "?token=" + url.QueryEscape(token)})
}
