package adminserver

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/backup"
	"github.com/515woodpl-dot/pibase/internal/control"
)

// maxImportBytes caps an uploaded export. Bigger projects move with the
// command line (scripts/import-project.sh), which has no upload in between.
const maxImportBytes = 4 << 30

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportProject builds an encrypted export file and sends it as a download.
func (s *Server) exportProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	var b struct {
		Passphrase string `json:"passphrase"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if len(b.Passphrase) < backup.MinPassphrase {
		http.Error(w, fmt.Sprintf("the passphrase must be at least %d characters", backup.MinPassphrase), http.StatusBadRequest)
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(time.Hour))
	if err := os.MkdirAll(s.BackupRoot, 0o700); err != nil {
		fail(w, err)
		return
	}
	// Build the whole file first so a failure is reported as an error rather
	// than as a truncated download.
	tmp, err := os.CreateTemp(s.BackupRoot, ".export-*.pibx")
	if err != nil {
		fail(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := backup.ExportProject(r.Context(), s.AdminURL, id, s.StorageRoot, s.MasterKey, b.Passphrase, tmp); err != nil {
		fail(w, err)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		fail(w, err)
		return
	}
	name := "pibase-" + id + "-" + time.Now().UTC().Format("20060102-1504") + ".pibx"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+unsafeFileChars.ReplaceAllString(name, "_")+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, tmp)
}

// importProject creates a project from an uploaded export file. The request
// is multipart: "passphrase" first, then "file".
func (s *Server) importProject(w http.ResponseWriter, r *http.Request) {
	ident, _ := getIdentity(r.Context())
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(time.Hour))
	_ = rc.SetWriteDeadline(time.Now().Add(time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "send the export file and passphrase as a form upload", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.BackupRoot, 0o700); err != nil {
		fail(w, err)
		return
	}
	var passphrase, path string
	defer func() {
		if path != "" {
			os.Remove(path)
		}
	}()
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "the file is larger than 4 GB; move this project with scripts/import-project.sh instead", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "the upload was interrupted", http.StatusBadRequest)
			return
		}
		switch part.FormName() {
		case "passphrase":
			data, _ := io.ReadAll(io.LimitReader(part, 1024))
			passphrase = string(data)
		case "file":
			tmp, err := os.CreateTemp(s.BackupRoot, ".import-*.pibx")
			if err != nil {
				fail(w, err)
				return
			}
			path = tmp.Name()
			_, copyErr := io.Copy(tmp, part)
			closeErr := tmp.Close()
			if copyErr != nil || closeErr != nil {
				var tooBig *http.MaxBytesError
				if errors.As(copyErr, &tooBig) {
					http.Error(w, "the file is larger than 4 GB; move this project with scripts/import-project.sh instead", http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, "the upload was interrupted", http.StatusBadRequest)
				return
			}
		}
		part.Close()
	}
	if path == "" || passphrase == "" {
		http.Error(w, "choose an export file and enter its passphrase", http.StatusBadRequest)
		return
	}
	opts := backup.ImportOptions{Operator: ident.kind == opKind}
	if ident.kind == accountKind {
		s.accountProjectsMu.Lock()
		defer s.accountProjectsMu.Unlock()
		n, err := control.CountOwnedProjects(r.Context(), s.AdminURL, ident.accountID)
		if err != nil {
			fail(w, err)
			return
		}
		if n >= s.MaxProjectsPerAccount {
			http.Error(w, fmt.Sprintf("project limit reached (%d per account); delete a project or ask the operator", s.MaxProjectsPerAccount), http.StatusForbidden)
			return
		}
		opts.OwnerAccountID = ident.accountID
	}
	result, err := backup.ImportProject(r.Context(), s.AdminURL, s.StorageRoot, s.MasterKey, path, passphrase, opts)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, backup.ErrBadPassphrase) {
			status = http.StatusUnprocessableEntity
		} else if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
