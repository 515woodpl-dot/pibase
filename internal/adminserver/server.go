// Package adminserver provides the loopback-only operator dashboard.  It is
// deliberately separate from the public gateway so its process is the only
// HTTP process that receives operator credentials.
package adminserver

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/backup"
	"github.com/515woodpl-dot/pibase/internal/control"
)

type Config struct {
	AdminURL, DatabaseURL, StorageRoot, BackupRoot, Token string
	MasterKey, BackupKey                                  []byte
}

type Server struct{ Config }

func New(c Config) (*Server, error) {
	if c.AdminURL == "" || c.DatabaseURL == "" || c.Token == "" || len(c.MasterKey) != 32 || len(c.BackupKey) != 32 {
		return nil, fmt.Errorf("admin-serve requires admin/database URLs, operator token, master key, and backup key")
	}
	if c.StorageRoot == "" {
		c.StorageRoot = "/data/files"
	}
	if c.BackupRoot == "" {
		c.BackupRoot = "/data/backups"
	}
	return &Server{Config: c}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dashboard", s.dashboard)
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("GET /_pibase/projects", s.projects)
	mux.HandleFunc("POST /_pibase/projects", s.createProject)
	mux.HandleFunc("DELETE /_pibase/projects/{id}", s.deleteProject)
	mux.HandleFunc("POST /_pibase/projects/{id}/sql", s.sql)
	mux.HandleFunc("POST /_pibase/projects/{id}/migrations", s.migration)
	mux.HandleFunc("POST /_pibase/projects/{id}/extensions/{extension}", s.extension)
	mux.HandleFunc("POST /_pibase/projects/{id}/backup", s.backup)
	mux.HandleFunc("GET /_pibase/projects/{id}/backups", s.backups)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "operator authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	const p = "Bearer "
	v := r.Header.Get("Authorization")
	return strings.HasPrefix(v, p) && len(v[len(p):]) == len(s.Token) && subtle.ConstantTimeCompare([]byte(v[len(p):]), []byte(s.Token)) == 1
}
func jsonBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(v); err != nil {
		http.Error(w, "invalid JSON", 400)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }

func (s *Server) dashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}
func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	p, err := control.ProjectStats(r.Context(), s.AdminURL, s.StorageRoot)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	p, err := control.CreateProject(r.Context(), s.AdminURL, b.Name, s.MasterKey)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, p)
}
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	id := r.PathValue("id")
	if b.Confirm != id {
		http.Error(w, "type the project id to delete", 400)
		return
	}
	if err := control.DeleteProject(r.Context(), s.AdminURL, id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) sql(w http.ResponseWriter, r *http.Request) {
	var b struct {
		SQL string `json:"sql"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	x, err := adminsql.Run(r.Context(), s.DatabaseURL, r.PathValue("id"), b.SQL)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) migration(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
		SQL  string `json:"sql"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if err := adminsql.ApplyMigration(r.Context(), s.DatabaseURL, r.PathValue("id"), b.Name, b.SQL); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) extension(w http.ResponseWriter, r *http.Request) {
	var err error
	switch r.PathValue("extension") {
	case "postgis":
		err = control.EnablePostGIS(r.Context(), s.AdminURL, r.PathValue("id"))
	case "pgvector":
		err = control.EnablePGVector(r.Context(), s.AdminURL, r.PathValue("id"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dst := filepath.Join(s.BackupRoot, id+"-"+time.Now().UTC().Format("20060102T150405Z")+".pibase")
	var k [32]byte
	copy(k[:], s.BackupKey)
	if err := backup.Backup(r.Context(), s.AdminURL, id, s.StorageRoot, dst, k); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"file": filepath.Base(dst)})
}
func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.BackupRoot)
	if err != nil && !os.IsNotExist(err) {
		fail(w, err)
		return
	}
	prefix := r.PathValue("id") + "-"
	out := make([]map[string]any, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		i, _ := e.Info()
		out = append(out, map[string]any{"name": e.Name(), "size": i.Size(), "time": i.ModTime()})
	}
	writeJSON(w, 200, out)
}

const page = `<!doctype html><meta charset="utf-8"><title>pibase operator</title><h1>pibase operator</h1><p>Operator token: <input id="token" type="password"><button id="load">Load projects</button></p><pre id="out"></pre><h2>Create project</h2><input id="name"><button id="create">Create</button><h2>Owner SQL / migrations</h2><p>Project id <input id="project"></p><textarea id="sql" rows="8" cols="80"></textarea><p><button id="run">Run SQL</button> <input id="migration" placeholder="migration name"><button id="migrate">Apply migration</button></p><h2>Extensions and backups</h2><button id="postgis">Enable PostGIS</button> <button id="vector">Enable pgvector</button> <button id="backup">Back up now</button><script>
const q=id=>document.getElementById(id), out=q('out'), auth=()=>({Authorization:'Bearer '+q('token').value,'Content-Type':'application/json'});async function call(path,opt={}){opt.headers={...auth(),...(opt.headers||{})};let r=await fetch(path,opt),t=await r.text();out.textContent=r.ok?t:r.status+' '+t;return r}q('load').onclick=()=>call('/_pibase/projects').then(async r=>{if(r.ok)out.textContent=JSON.stringify(await r.json(),null,2)});q('create').onclick=()=>call('/_pibase/projects',{method:'POST',body:JSON.stringify({name:q('name').value})});q('run').onclick=()=>call('/_pibase/projects/'+encodeURIComponent(q('project').value)+'/sql',{method:'POST',body:JSON.stringify({sql:q('sql').value})});q('migrate').onclick=()=>call('/_pibase/projects/'+encodeURIComponent(q('project').value)+'/migrations',{method:'POST',body:JSON.stringify({name:q('migration').value,sql:q('sql').value})});q('postgis').onclick=()=>call('/_pibase/projects/'+encodeURIComponent(q('project').value)+'/extensions/postgis',{method:'POST'});q('vector').onclick=()=>call('/_pibase/projects/'+encodeURIComponent(q('project').value)+'/extensions/pgvector',{method:'POST'});q('backup').onclick=()=>call('/_pibase/projects/'+encodeURIComponent(q('project').value)+'/backup',{method:'POST'});</script>`
