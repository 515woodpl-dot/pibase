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
		// The page itself is static and holds no data or secrets. A browser
		// cannot send the bearer token on navigation, so only the page is
		// served without it; every /_pibase/ endpoint requires the token.
		if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/dashboard") {
			w.Header().Set("Cache-Control", "no-store")
			s.dashboard(w, r)
			return
		}
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
	var k [32]byte
	copy(k[:], s.BackupKey)
	// The backup package takes a destination directory and names the file.
	file, err := backup.BackupTo(r.Context(), s.AdminURL, r.PathValue("id"), s.StorageRoot, s.BackupRoot, k)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"file": filepath.Base(file)})
}
func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.BackupRoot)
	if err != nil && !os.IsNotExist(err) {
		fail(w, err)
		return
	}
	id := r.PathValue("id")
	out := make([]map[string]any, 0)
	for _, e := range entries {
		name := e.Name()
		ours := strings.HasPrefix(name, "pibase-"+id+"-") || strings.HasPrefix(name, "pre-restore-"+id+"-")
		if e.IsDir() || !ours || !strings.HasSuffix(name, ".pib") {
			continue
		}
		i, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"name": name, "size": i.Size(), "time": i.ModTime()})
	}
	writeJSON(w, 200, out)
}

const page = `<!doctype html><meta charset="utf-8"><title>pibase operator</title><h1>pibase operator</h1>
<p>Operator token: <input id="token" type="password"> <button id="load">Load projects</button></p>
<h2>Create project</h2><p>Name <input id="name"> <button id="create">Create</button> (keys are shown once, below)</p>
<h2>Selected project</h2><p>Project id <input id="project"></p>
<p><button id="postgis">Enable PostGIS</button> <button id="vector">Enable pgvector</button> <button id="backup">Back up now</button> <button id="backups">List backups</button> <button id="migrations">List applied migrations</button></p>
<p>Delete: type the project id again <input id="confirm"> <button id="delete">Delete project</button></p>
<h2>Owner SQL / migration</h2><textarea id="sql" rows="8" cols="80"></textarea>
<p><button id="run">Run SQL</button> Migration name <input id="migration"> <button id="migrate">Apply as migration</button></p>
<h2>Result</h2><pre id="out"></pre>
<script>
const q=id=>document.getElementById(id), out=q('out');
const auth=()=>({Authorization:'Bearer '+q('token').value,'Content-Type':'application/json'});
const proj=()=>'/_pibase/projects/'+encodeURIComponent(q('project').value);
async function call(path,opt={}){
  opt.headers=auth(); out.textContent='Working...';
  try{
    const r=await fetch(path,opt), t=await r.text(); let shown=t;
    if(r.ok){ try{ shown=JSON.stringify(JSON.parse(t),null,2) }catch(e){} if(!shown) shown='Done ('+r.status+')'; }
    else shown='Error '+r.status+': '+t;
    out.textContent=shown; return r;
  }catch(e){ out.textContent='Request failed: '+e }
}
const body=o=>({method:'POST',body:JSON.stringify(o)});
q('load').onclick=()=>call('/_pibase/projects');
q('create').onclick=()=>call('/_pibase/projects',body({name:q('name').value}));
q('run').onclick=()=>call(proj()+'/sql',body({sql:q('sql').value}));
q('migrate').onclick=()=>call(proj()+'/migrations',body({name:q('migration').value,sql:q('sql').value}));
q('migrations').onclick=()=>call(proj()+'/sql',body({sql:'SELECT name, applied_at FROM public.pibase_migrations ORDER BY applied_at'}));
q('postgis').onclick=()=>call(proj()+'/extensions/postgis',{method:'POST'});
q('vector').onclick=()=>call(proj()+'/extensions/pgvector',{method:'POST'});
q('backup').onclick=()=>call(proj()+'/backup',{method:'POST'});
q('backups').onclick=()=>call(proj()+'/backups');
q('delete').onclick=()=>call(proj(),{method:'DELETE',body:JSON.stringify({confirm:q('confirm').value})});
</script>`
