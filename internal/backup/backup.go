package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

type Manifest struct {
	Version   int       `json:"version"`
	ProjectID string    `json:"project_id"`
	Database  string    `json:"database"`
	CreatedAt time.Time `json:"created_at"`
}

func Backup(ctx context.Context, adminURL, projectID, storageRoot, destination string, key [32]byte) (err error) {
	database, err := projectDatabase(ctx, adminURL, projectID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	work, err := os.MkdirTemp(destination, ".pibase-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	dump := filepath.Join(work, "database.dump")
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--file", dump, withDatabase(adminURL, database))
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("pg_dump: %w: %s", runErr, strings.TrimSpace(string(output)))
	}
	final := filepath.Join(destination, fmt.Sprintf("pibase-%s-%s.pib", projectID, time.Now().UTC().Format("20060102T150405Z")))
	temp := final + ".partial"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(temp)
		}
	}()
	encrypted, err := newEncryptWriter(file, key)
	if err != nil {
		return err
	}
	archive := tar.NewWriter(encrypted)
	manifest, _ := json.Marshal(Manifest{1, projectID, database, time.Now().UTC()})
	if err = writeBytes(archive, "manifest.json", manifest, 0o600); err != nil {
		return err
	}
	if err = writeFile(archive, "database.dump", dump); err != nil {
		return err
	}
	root := filepath.Join(storageRoot, projectID)
	if err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		return writeFile(archive, filepath.ToSlash(filepath.Join("storage", rel)), path)
	}); err != nil {
		return err
	}
	if err = archive.Close(); err != nil {
		return err
	}
	if err = encrypted.Close(); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp, final); err != nil {
		return err
	}
	fmt.Println(final)
	return nil
}

func Restore(ctx context.Context, adminURL, projectID, storageRoot, source string, key [32]byte) error {
	database, err := projectDatabase(ctx, adminURL, projectID)
	if err != nil {
		return err
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	decrypted, err := newDecryptReader(file, key)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "pibase-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	archive := tar.NewReader(decrypted)
	var manifest Manifest
	for {
		header, e := archive.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		clean := filepath.Clean(header.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe backup path")
		}
		target := filepath.Join(work, clean)
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported backup entry %q", header.Name)
		}
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if e != nil {
			return e
		}
		_, copyErr := io.Copy(out, archive)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if clean == "manifest.json" {
			data, _ := os.ReadFile(target)
			if err = json.Unmarshal(data, &manifest); err != nil {
				return err
			}
		}
	}
	if _, err = io.Copy(io.Discard, decrypted); err != nil {
		return err
	}
	if manifest.Version != 1 || manifest.ProjectID != projectID || manifest.Database != database {
		return fmt.Errorf("backup belongs to project %q, not %q", manifest.ProjectID, projectID)
	}
	dump := filepath.Join(work, "database.dump")
	cmd := exec.CommandContext(ctx, "pg_restore", "--clean", "--if-exists", "--dbname", withDatabase(adminURL, database), dump)
	if output, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("pg_restore: %w: %s", e, strings.TrimSpace(string(output)))
	}
	// A pre-M6c archive restores customer ownership of the managed schemas.
	// Reapply the canonical upgrade before returning the project to service.
	if err = control.UpgradeProjectSchema(ctx, adminURL, projectID); err != nil {
		return fmt.Errorf("upgrade restored project schema: %w", err)
	}
	newStorage := filepath.Join(work, "storage")
	target := filepath.Join(storageRoot, projectID)
	old := target + ".before-restore-" + time.Now().UTC().Format("20060102T150405Z")
	if _, e := os.Stat(target); e == nil {
		if e = os.Rename(target, old); e != nil {
			return e
		}
	}
	if makeErr := os.MkdirAll(filepath.Dir(target), 0o750); makeErr != nil {
		return makeErr
	}
	if _, statErr := os.Stat(newStorage); os.IsNotExist(statErr) {
		return os.MkdirAll(target, 0o750)
	}
	return copyTree(newStorage, target)
}

func projectDatabase(ctx context.Context, adminURL, id string) (string, error) {
	conn, err := pgx.Connect(ctx, withDatabase(adminURL, "_control"))
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var db string
	if err = conn.QueryRow(ctx, "SELECT database_name FROM control.projects WHERE id=$1", id).Scan(&db); err != nil {
		return "", fmt.Errorf("project %q not found: %w", id, err)
	}
	return db, nil
}
func withDatabase(raw, database string) string {
	idx := strings.LastIndex(raw, "/")
	if idx < 0 {
		return raw
	}
	suffix := ""
	if q := strings.Index(raw[idx:], "?"); q >= 0 {
		suffix = raw[idx+q:]
	}
	return raw[:idx+1] + database + suffix
}
func writeBytes(t *tar.Writer, name string, data []byte, mode int64) error {
	if err := t.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := t.Write(data)
	return err
}
func writeFile(t *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err = t.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(t, f)
	return err
}
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		in, e := os.Open(path)
		if e != nil {
			return e
		}
		defer in.Close()
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, in)
		if closeErr := out.Close(); e == nil {
			e = closeErr
		}
		return e
	})
}
