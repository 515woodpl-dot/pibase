package backup

// Moving a project to another PIBASE server.
//
// An export file holds everything that makes a project the same project
// elsewhere: its public schema (tables, data, functions, policies), PIBASE's
// own data for it (users, sessions, 2FA factors, buckets and file records,
// scheduled jobs), its files, its settings, and its ID, signing secret and API
// keys, so websites and signed-in users keep working. It is encrypted with a
// passphrase the person chooses, not with either server's keys.
//
// An import file comes from a user, so nothing in it runs with server
// rights:
//   - The public schema is restored over a temporary login that can only act
//     as the new project's admin role, the same rights the project's owner
//     has in the SQL editor.
//   - PIBASE's own tables are loaded row by row from JSON by this code, with
//     parameters, never as SQL from the file.
//   - 2FA secrets travel decrypted inside the file and are encrypted again
//     with this server's key.
//   - The keys must be signed by the file's secret, and neither the project
//     ID nor the keys may already exist here.

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

var exportMagic = []byte("PIBASEX1")

// MinPassphrase is the shortest passphrase an export accepts.
const MinPassphrase = 12

// managedSchemas hold PIBASE's own data for a project.
var managedSchemas = []string{"auth", "storage", "realtime", "cron"}

// ExportManifest describes an export file.
type ExportManifest struct {
	Kind             string    `json:"kind"`
	Version          int       `json:"version"`
	ProjectID        string    `json:"project_id"`
	Name             string    `json:"name"`
	SchemaVersion    int       `json:"schema_version"`
	MaxDatabaseBytes int64     `json:"max_database_bytes"`
	FunctionsURL     string    `json:"functions_url,omitempty"`
	PublicURL        string    `json:"public_url,omitempty"`
	Extensions       []string  `json:"extensions,omitempty"`
	Tables           []string  `json:"managed_tables"`
	CreatedAt        time.Time `json:"created_at"`
}

type exportSecrets struct {
	JWTSecret  string            `json:"jwt_secret"`
	AnonKey    string            `json:"anon_key"`
	ServiceKey string            `json:"service_key"`
	MFASecrets map[string]string `json:"mfa_secrets,omitempty"` // factor id -> base64 secret
}

func passphraseKey(passphrase string, salt []byte) [32]byte {
	var key [32]byte
	copy(key[:], argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 2, 32))
	return key
}

// ExportProject writes an encrypted export of the project to out.
func ExportProject(ctx context.Context, adminURL, projectID, storageRoot string, masterKey []byte, passphrase string, out io.Writer) error {
	if len(passphrase) < MinPassphrase {
		return fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	control_, err := pgx.Connect(ctx, withDatabase(adminURL, "_control"))
	if err != nil {
		return err
	}
	m := ExportManifest{Kind: "pibase-project-export", Version: 1, ProjectID: projectID, CreatedAt: time.Now().UTC()}
	var database string
	err = control_.QueryRow(ctx, `SELECT name, database_name, schema_version, max_database_bytes, COALESCE(functions_url,''), COALESCE(public_url,'')
FROM control.projects WHERE id=$1`, projectID).Scan(&m.Name, &database, &m.SchemaVersion, &m.MaxDatabaseBytes, &m.FunctionsURL, &m.PublicURL)
	control_.Close(ctx)
	if err != nil {
		return fmt.Errorf("project %q not found", projectID)
	}
	k, err := control.GetProjectKeys(ctx, adminURL, projectID, masterKey)
	if err != nil {
		return err
	}
	if k.AnonKey == "" || k.ServiceKey == "" {
		return fmt.Errorf("this project's keys predate key storage; rotate its keys once, then export")
	}
	secrets := exportSecrets{JWTSecret: k.JWTSecret, AnonKey: k.AnonKey, ServiceKey: k.ServiceKey, MFASecrets: map[string]string{}}

	work, err := os.MkdirTemp("", "pibase-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	project, err := pgx.Connect(ctx, withDatabase(adminURL, database))
	if err != nil {
		return err
	}
	defer project.Close(ctx)
	rows, err := project.Query(ctx, `SELECT extname FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY 1`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		m.Extensions = append(m.Extensions, name)
	}
	rows.Close()
	tables, err := managedTables(ctx, project)
	if err != nil {
		return err
	}
	m.Tables = tables
	for _, table := range tables {
		if err = dumpTableJSON(ctx, project, table, filepath.Join(work, "managed", table+".jsonl")); err != nil {
			return err
		}
	}
	// 2FA secrets are encrypted with this server's key; carry them decrypted
	// (inside the passphrase-encrypted file) so the new server can re-encrypt.
	mfa, err := project.Query(ctx, `SELECT id::text, secret_encrypted FROM auth.mfa_factors`)
	if err == nil {
		for mfa.Next() {
			var id string
			var encrypted []byte
			if err = mfa.Scan(&id, &encrypted); err != nil {
				mfa.Close()
				return err
			}
			plain, e := secretbox.Decrypt(masterKey, encrypted)
			if e != nil {
				mfa.Close()
				return fmt.Errorf("decrypt a 2FA secret: %w", e)
			}
			secrets.MFASecrets[id] = base64.StdEncoding.EncodeToString(plain)
		}
		mfa.Close()
		if err = mfa.Err(); err != nil {
			return err
		}
	}
	dump := filepath.Join(work, "public.dump")
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--schema=public", "--no-owner", "--file", dump, withDatabase(adminURL, database))
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("pg_dump: %w: %s", runErr, strings.TrimSpace(string(output)))
	}

	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return err
	}
	if _, err = out.Write(append(append([]byte{}, exportMagic...), salt...)); err != nil {
		return err
	}
	encrypted, err := newEncryptWriter(out, passphraseKey(passphrase, salt))
	if err != nil {
		return err
	}
	archive := tar.NewWriter(encrypted)
	manifest, _ := json.MarshalIndent(m, "", "  ")
	secretJSON, _ := json.Marshal(secrets)
	if err = writeBytes(archive, "manifest.json", manifest, 0o600); err != nil {
		return err
	}
	if err = writeBytes(archive, "secrets.json", secretJSON, 0o600); err != nil {
		return err
	}
	if err = writeFile(archive, "public.dump", dump); err != nil {
		return err
	}
	for _, table := range tables {
		if err = writeFile(archive, "managed/"+table+".jsonl", filepath.Join(work, "managed", table+".jsonl")); err != nil {
			return err
		}
	}
	root := filepath.Join(storageRoot, projectID)
	if err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || strings.Contains(info.Name(), ".tmp-") {
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
	return encrypted.Close()
}

func managedTables(ctx context.Context, db *pgx.Conn) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT table_schema||'.'||table_name FROM information_schema.tables
WHERE table_type='BASE TABLE' AND table_schema = ANY($1) ORDER BY 1`, managedSchemas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func dumpTableJSON(ctx context.Context, db *pgx.Conn, table, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	schema, name, _ := strings.Cut(table, ".")
	ident := pgx.Identifier{schema, name}.Sanitize()
	rows, err := db.Query(ctx, `SELECT to_jsonb(t)::text FROM `+ident+` t`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return w.Flush()
}

// ImportOptions control who the imported project belongs to and what it may
// bring along.
type ImportOptions struct {
	// OwnerAccountID owns the new project (empty for an operator import).
	OwnerAccountID string
	// Operator imports may bring database extensions (PostGIS, pgvector) and
	// a server-side code address on a private network.
	Operator bool
}

// ImportResult describes the imported project.
type ImportResult struct {
	ProjectID string   `json:"project_id"`
	Name      string   `json:"name"`
	Notes     []string `json:"notes,omitempty"`
}

// ErrBadPassphrase means the file could not be decrypted.
var ErrBadPassphrase = errors.New("this file can't be opened with that passphrase, or it is not a PIBASE export")

// ImportProject creates a project on this server from an export file.
func ImportProject(ctx context.Context, adminURL, storageRoot string, masterKey []byte, source, passphrase string, opts ImportOptions) (result ImportResult, err error) {
	file, err := os.Open(source)
	if err != nil {
		return result, err
	}
	defer file.Close()
	header := make([]byte, len(exportMagic)+16)
	if _, err = io.ReadFull(file, header); err != nil || string(header[:len(exportMagic)]) != string(exportMagic) {
		return result, ErrBadPassphrase
	}
	decrypted, err := newDecryptReader(file, passphraseKey(passphrase, header[len(exportMagic):]))
	if err != nil {
		return result, ErrBadPassphrase
	}
	work, err := os.MkdirTemp("", "pibase-import-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	if err = unpack(decrypted, work); err != nil {
		if strings.Contains(err.Error(), "authentication") || strings.Contains(err.Error(), "decrypt") || strings.Contains(err.Error(), "backup frame") {
			return result, ErrBadPassphrase
		}
		return result, err
	}
	var m ExportManifest
	var secrets exportSecrets
	if err = readJSON(filepath.Join(work, "manifest.json"), &m); err != nil {
		return result, ErrBadPassphrase
	}
	if m.Kind != "pibase-project-export" || m.Version != 1 {
		return result, fmt.Errorf("this file is not a PIBASE project export this server understands")
	}
	if err = readJSON(filepath.Join(work, "secrets.json"), &secrets); err != nil {
		return result, fmt.Errorf("the export is incomplete")
	}
	if m.SchemaVersion > control.CurrentProjectSchemaVersion() {
		return result, fmt.Errorf("this export comes from a newer PIBASE; update this server first")
	}
	var extensions []string
	for _, e := range m.Extensions {
		if e != "plpgsql" {
			extensions = append(extensions, e)
		}
	}
	if len(extensions) > 0 && !opts.Operator {
		return result, fmt.Errorf("this project uses %s; ask the server operator to import it", strings.Join(extensions, " and "))
	}
	for _, e := range extensions {
		if e != "postgis" && e != "vector" {
			return result, fmt.Errorf("this project uses the %s extension, which PIBASE can't install", e)
		}
	}
	jwtSecret, err := base64.StdEncoding.DecodeString(secrets.JWTSecret)
	if err != nil {
		return result, fmt.Errorf("the export's signing secret is damaged")
	}
	created, err := control.CreateProjectWithIdentity(ctx, adminURL, m.Name, masterKey, control.ProjectIdentity{
		ID: m.ProjectID, JWTSecret: jwtSecret, AnonKey: secrets.AnonKey, ServiceKey: secrets.ServiceKey})
	if err != nil {
		if errors.Is(err, control.ErrDuplicate) {
			return result, fmt.Errorf("project %s (or its keys) already exists on this server; delete it first to import it again", m.ProjectID)
		}
		return result, err
	}
	result = ImportResult{ProjectID: created.ID, Name: created.Name}
	defer func() {
		if err != nil {
			_ = control.DeleteProject(context.Background(), adminURL, created.ID)
			_ = os.RemoveAll(filepath.Join(storageRoot, created.ID))
		}
	}()
	if opts.OwnerAccountID != "" {
		if err = control.SetProjectOwner(ctx, adminURL, created.ID, opts.OwnerAccountID); err != nil {
			return result, err
		}
	}
	for _, e := range extensions {
		if e == "postgis" {
			err = control.EnablePostGIS(ctx, adminURL, created.ID)
		} else {
			err = control.EnablePGVector(ctx, adminURL, created.ID)
		}
		if err != nil {
			return result, fmt.Errorf("enable %s: %w", e, err)
		}
	}
	if err = loadManaged(ctx, adminURL, created.Database, work, m.Tables, secrets.MFASecrets, masterKey); err != nil {
		return result, err
	}
	if err = restorePublic(ctx, adminURL, created.ID, created.Database, filepath.Join(work, "public.dump")); err != nil {
		return result, err
	}
	if err = control.UpgradeProjectSchema(ctx, adminURL, created.ID); err != nil {
		return result, fmt.Errorf("finish schema: %w", err)
	}
	if m.MaxDatabaseBytes > 0 {
		if err = control.SetLimits(ctx, adminURL, created.ID, m.MaxDatabaseBytes); err != nil {
			return result, err
		}
	}
	if m.PublicURL != "" {
		if _, e := control.SetPublicURL(ctx, adminURL, created.ID, m.PublicURL); e != nil {
			result.Notes = append(result.Notes, "API address not kept: "+e.Error())
		}
	}
	if m.FunctionsURL != "" {
		if _, e := control.SetFunctionsURL(ctx, adminURL, created.ID, m.FunctionsURL, opts.Operator); e != nil {
			result.Notes = append(result.Notes, "Server-side code address not kept ("+e.Error()+"); set it again in Settings.")
		}
	}
	files := filepath.Join(work, "storage")
	target := filepath.Join(storageRoot, created.ID)
	if _, statErr := os.Stat(files); statErr == nil {
		if err = copyTree(files, target); err != nil {
			return result, err
		}
	} else if err = os.MkdirAll(target, 0o750); err != nil {
		return result, err
	}
	return result, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// unpack extracts regular files only, refusing paths that leave dir.
func unpack(r io.Reader, dir string) error {
	archive := tar.NewReader(r)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(header.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || header.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsafe entry in export: %q", header.Name)
		}
		target := filepath.Join(dir, clean)
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, archive)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	// Read to the end so the final frame's authentication is checked.
	_, err := io.Copy(io.Discard, r)
	return err
}

// loadManaged inserts PIBASE's own rows from JSON. Table names come from this
// server's own catalog (a file can't name a table that isn't a managed table
// here), columns are limited to those that exist here, and values are bound
// parameters.
func loadManaged(ctx context.Context, adminURL, database, work string, exported []string, mfa map[string]string, masterKey []byte) error {
	db, err := pgx.Connect(ctx, withDatabase(adminURL, database))
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	present, err := managedTables(ctx, db)
	if err != nil {
		return err
	}
	here := map[string]bool{}
	for _, t := range present {
		here[t] = true
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Rows arrive in no particular order; foreign keys are checked by the
	// source database already. Triggers (and so constraint checks) are off
	// for this transaction only.
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return err
	}
	sorted := append([]string(nil), exported...)
	sort.Strings(sorted)
	for _, table := range sorted {
		if !here[table] {
			continue
		}
		path := filepath.Join(work, "managed", table+".jsonl")
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}
		if err = loadTable(ctx, tx, table, path); err != nil {
			return fmt.Errorf("load %s: %w", table, err)
		}
	}
	for id, b64 := range mfa {
		plain, e := base64.StdEncoding.DecodeString(b64)
		if e != nil {
			return fmt.Errorf("a 2FA secret in the export is damaged")
		}
		encrypted, e := secretbox.Encrypt(masterKey, plain)
		if e != nil {
			return e
		}
		if _, err = tx.Exec(ctx, `UPDATE auth.mfa_factors SET secret_encrypted=$1 WHERE id=$2::uuid`, encrypted, id); err != nil {
			return err
		}
	}
	// Identity and serial columns continue after the imported rows.
	seqs, err := tx.Query(ctx, `SELECT c.table_schema, c.table_name, c.column_name FROM information_schema.columns c
WHERE c.table_schema = ANY($1) AND (c.is_identity='YES' OR c.column_default LIKE 'nextval(%')`, managedSchemas)
	if err != nil {
		return err
	}
	type col struct{ schema, table, column string }
	var cols []col
	for seqs.Next() {
		var c col
		if err = seqs.Scan(&c.schema, &c.table, &c.column); err != nil {
			seqs.Close()
			return err
		}
		cols = append(cols, c)
	}
	seqs.Close()
	for _, c := range cols {
		ident := pgx.Identifier{c.schema, c.table}.Sanitize()
		column := pgx.Identifier{c.column}.Sanitize()
		if _, err = tx.Exec(ctx, `SELECT setval(pg_get_serial_sequence($1, $2), GREATEST((SELECT max(`+column+`) FROM `+ident+`), 1))`,
			ident, c.column); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func loadTable(ctx context.Context, tx pgx.Tx, table, path string) error {
	schema, name, _ := strings.Cut(table, ".")
	ident := pgx.Identifier{schema, name}.Sanitize()
	rows, err := tx.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2
AND is_generated='NEVER'`, schema, name)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var c string
		if err = rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		columns[c] = true
	}
	rows.Close()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		var row map[string]json.RawMessage
		if err = json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("damaged row: %w", err)
		}
		var names []string
		for k := range row {
			if columns[k] {
				names = append(names, k)
			}
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = pgx.Identifier{n}.Sanitize()
		}
		list := strings.Join(quoted, ",")
		if _, err = tx.Exec(ctx, `INSERT INTO `+ident+` (`+list+`) OVERRIDING SYSTEM VALUE SELECT `+list+` FROM jsonb_populate_record(NULL::`+ident+`, $1::jsonb)`, string(line)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// restorePublic restores the exported public schema as the project's admin
// role, through a temporary login that can do nothing else.
func restorePublic(ctx context.Context, adminURL, projectID, database, dump string) error {
	admin := "p_" + projectID + "_admin"
	login := "p_" + projectID + "_import"
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		return err
	}
	password := hex.EncodeToString(passwordBytes)
	root, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer root.Close(ctx)
	quotedLogin := pgx.Identifier{login}.Sanitize()
	if _, err = root.Exec(ctx, `CREATE ROLE `+quotedLogin+` LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT 2 PASSWORD '`+password+`'`); err != nil {
		return err
	}
	defer func() {
		cleanup := context.Background()
		_, _ = root.Exec(cleanup, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename=$1`, login)
		_, _ = root.Exec(cleanup, `DROP OWNED BY `+quotedLogin)
		_, _ = root.Exec(cleanup, `DROP ROLE IF EXISTS `+quotedLogin)
	}()
	if _, err = root.Exec(ctx, `GRANT `+pgx.Identifier{admin}.Sanitize()+` TO `+quotedLogin); err != nil {
		return err
	}
	if _, err = root.Exec(ctx, `GRANT CONNECT ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO `+quotedLogin); err != nil {
		return err
	}
	u, err := url.Parse(withDatabase(adminURL, database))
	if err != nil {
		return err
	}
	u.User = url.UserPassword(login, password)
	// The new project already has its public schema (with PIBASE's grants),
	// so leave out the dump's entries for the schema itself.
	listing, err := exec.CommandContext(ctx, "pg_restore", "--list", dump).Output()
	if err != nil {
		return fmt.Errorf("read the export's table list: %w", err)
	}
	var keep []string
	for _, line := range strings.Split(string(listing), "\n") {
		if strings.Contains(line, " SCHEMA - public ") {
			continue
		}
		keep = append(keep, line)
	}
	listFile := dump + ".list"
	if err = os.WriteFile(listFile, []byte(strings.Join(keep, "\n")), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "pg_restore", "--no-owner", "--role="+admin, "--single-transaction", "--exit-on-error", "--use-list", listFile, "--dbname", u.String(), dump)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("restore the project's tables: %s", strings.TrimSpace(redact(string(output), password)))
	}
	return nil
}

func redact(s, secret string) string { return strings.ReplaceAll(s, secret, "***") }
