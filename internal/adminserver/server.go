// Package adminserver provides the loopback-only operator dashboard.  It is
// deliberately separate from the public gateway so its process is the only
// HTTP process that receives operator credentials.
package adminserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/515woodpl-dot/pibase/internal/backup"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
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
	mux.HandleFunc("GET /_pibase/projects/{id}/tables", s.tables)
	mux.HandleFunc("GET /_pibase/projects/{id}/tables/{table}/rows", s.rows)
	mux.HandleFunc("GET /_pibase/projects/{id}/migrations", s.migrations)
	mux.HandleFunc("GET /_pibase/projects/{id}/users", s.users)
	mux.HandleFunc("POST /_pibase/projects/{id}/users", s.createUser)
	mux.HandleFunc("DELETE /_pibase/projects/{id}/users/{user}", s.deleteUser)
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

type tableInfo struct {
	Name         string   `json:"name"`
	Count        int64    `json:"count"`
	RLS          bool     `json:"rls"`
	AnonReadable bool     `json:"anon_readable"`
	PrimaryKey   []string `json:"primary_key"`
	Columns      []string `json:"columns"`
}

func (s *Server) tables(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := adminsql.RunParams(r.Context(), s.DatabaseURL, id, `
SELECT c.relname AS name, c.relrowsecurity AS rls,
 COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM pg_index i JOIN unnest(i.indkey) WITH ORDINALITY k(attnum,ord) ON true JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum WHERE i.indrelid=c.oid AND i.indisprimary), ARRAY[]::text[]) AS primary_key,
 (SELECT array_agg(a.attname ORDER BY a.attnum) FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped) AS columns
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname <> 'pibase_migrations' ORDER BY c.relname`)
	if err != nil {
		fail(w, err)
		return
	}
	tables := make([]tableInfo, 0, len(result.Rows))
	for _, row := range result.Rows {
		tables = append(tables, tableInfo{
			Name:       row["name"].(string),
			RLS:        row["rls"] == true,
			PrimaryKey: stringsOf(row["primary_key"]),
			Columns:    stringsOf(row["columns"]),
		})
	}
	entries := make([]adminsql.TableEntry, len(tables))
	for i, t := range tables {
		entries[i] = adminsql.TableEntry{Name: t.Name, RLS: t.RLS}
	}
	anonFlags, counts, err := adminsql.TableMeta(r.Context(), s.DatabaseURL, id, entries)
	if err != nil {
		fail(w, err)
		return
	}
	for i := range tables {
		tables[i].Count = counts[i]
		tables[i].AnonReadable = anonFlags[i]
	}
	writeJSON(w, 200, tables)
}

func (s *Server) rows(w http.ResponseWriter, r *http.Request) {
	t, err := s.table(r)
	if err != nil {
		fail(w, err)
		return
	}
	offset := 0
	fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
	if offset < 0 {
		offset = 0
	}
	x, err := adminsql.BrowseRows(r.Context(), s.DatabaseURL, r.PathValue("id"), t.Name, t.Columns, t.PrimaryKey, offset)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, x)
}

func (s *Server) table(r *http.Request) (tableInfo, error) {
	x, err := adminsql.RunParams(r.Context(), s.DatabaseURL, r.PathValue("id"), `SELECT c.relname AS name, COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM pg_index i JOIN unnest(i.indkey) WITH ORDINALITY k(attnum,ord) ON true JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum WHERE i.indrelid=c.oid AND i.indisprimary), ARRAY[]::text[]) AS primary_key, (SELECT array_agg(a.attname ORDER BY a.attnum) FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped) AS columns FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname=$1`, r.PathValue("table"))
	if err != nil {
		return tableInfo{}, err
	}
	if len(x.Rows) != 1 {
		return tableInfo{}, fmt.Errorf("table not found")
	}
	return tableInfo{Name: r.PathValue("table"), PrimaryKey: stringsOf(x.Rows[0]["primary_key"]), Columns: stringsOf(x.Rows[0]["columns"])}, nil
}
func quoteIdent(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func stringsOf(v any) []string {
	a, ok := v.([]string)
	if ok {
		return a
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// User management deliberately uses the administrator connection, because the
// auth schema is operator-owned and is not accessible to project owner SQL.
// Its statements are fixed and all caller values are bound.
func (s *Server) projectDB(ctx context.Context, id string) (*pgx.Conn, error) {
	controlURL, err := url.Parse(s.AdminURL)
	if err != nil {
		return nil, err
	}
	controlURL.Path = "/_control"
	c, err := pgx.Connect(ctx, controlURL.String())
	if err != nil {
		return nil, err
	}
	defer c.Close(ctx)
	var database string
	if err = c.QueryRow(ctx, `SELECT database_name FROM control.project_catalog WHERE id=$1`, id).Scan(&database); err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}
	u, err := url.Parse(s.AdminURL)
	if err != nil {
		return nil, err
	}
	u.Path = "/" + database
	return pgx.Connect(ctx, u.String())
}
func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	c, err := s.projectDB(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	defer c.Close(r.Context())
	rows, err := c.Query(r.Context(), `SELECT id,email,email_confirmed_at,banned_until,created_at FROM auth.users ORDER BY created_at`)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, email string
		var confirmed, banned *time.Time
		var created time.Time
		if err := rows.Scan(&id, &email, &confirmed, &banned, &created); err != nil {
			fail(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "email": email, "email_confirmed_at": confirmed, "banned_until": banned, "created_at": created})
	}
	if err := rows.Err(); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email        string `json:"email"`
		Password     string `json:"password"`
		EmailConfirm bool   `json:"email_confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	if b.Email == "" || len(b.Password) < 8 {
		fail(w, fmt.Errorf("email and a password of at least 8 characters are required"))
		return
	}
	hash, err := auth.HashPassword(b.Password)
	if err != nil {
		fail(w, err)
		return
	}
	c, err := s.projectDB(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	defer c.Close(r.Context())
	id, err := newUUID()
	if err != nil {
		fail(w, err)
		return
	}
	var confirmed any
	if b.EmailConfirm {
		confirmed = time.Now().UTC()
	}
	_, err = c.Exec(r.Context(), `INSERT INTO auth.users(id,email,password_hash,email_confirmed_at) VALUES($1,$2,$3,$4)`, id, b.Email, hash, confirmed)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "email": b.Email})
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16]), nil
}
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	id := r.PathValue("user")
	if b.Confirm != id {
		fail(w, fmt.Errorf("type the user id to delete"))
		return
	}
	c, err := s.projectDB(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	defer c.Close(r.Context())
	tag, err := c.Exec(r.Context(), `DELETE FROM auth.users WHERE id=$1`, id)
	if err != nil {
		fail(w, err)
		return
	}
	if tag.RowsAffected() != 1 {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(204)
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
func (s *Server) migrations(w http.ResponseWriter, r *http.Request) {
	x, err := adminsql.RunParams(r.Context(), s.DatabaseURL, r.PathValue("id"), `SELECT name, left(checksum,8) AS checksum, applied_at FROM public.pibase_migrations ORDER BY applied_at`)
	if err != nil {
		writeJSON(w, 200, []any{})
		return
	}
	writeJSON(w, 200, x.Rows)
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

const page = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>pibase operator</title><style>
:root{--ink:#333;--muted:#777;--line:#e8e8e8;--tint:#f3f6f8;font:14px system-ui,sans-serif;color:var(--ink);background:#fff}*{box-sizing:border-box}body{margin:0}button,a,.menu{font:inherit;color:inherit;-webkit-tap-highlight-color:transparent;user-select:none}button{background:#fff;border:1px solid var(--line);padding:9px 12px;cursor:pointer}button:focus{outline:none}button:focus-visible,a:focus-visible,.menu:focus-visible{outline:none;background:var(--tint);text-decoration:underline}input,textarea{font:inherit;color:var(--ink);border:1px solid var(--line);padding:9px;width:100%}textarea{min-height:150px}.side{position:fixed;inset:0 auto 0 0;width:210px;border-right:1px solid var(--line);padding:32px 20px;background:#fff;z-index:1}.brand{font-size:18px;margin-bottom:34px}.menu{display:block;border:0;background:transparent;padding:9px 0;text-align:left;width:100%;cursor:pointer}.menu.secondary{color:var(--muted);padding-left:12px}.muted{color:var(--muted)}.app{margin-left:210px}.token{border-bottom:1px solid var(--line);padding:24px;display:flex;gap:12px;align-items:center}.token input{max-width:360px}.chain{display:flex;min-height:calc(100vh - 73px);overflow-x:auto}.pane{flex:0 0 390px;border-right:1px solid var(--line);padding:32px 24px;min-height:100%}.pane h1{font-size:18px;font-weight:600;margin:0 0 28px}.group{margin:0 0 28px}.group>*+*{margin-top:10px}.item{display:block;width:100%;text-align:left;border-width:0 0 1px;padding:13px 0}.item span{display:block;color:var(--muted);font-size:12px;margin-top:4px}.row{display:flex;gap:10px;align-items:center}.row>*{min-width:0}.row input{flex:1}.danger{color:#8b3333}.data{overflow:auto;border-top:1px solid var(--line);border-bottom:1px solid var(--line)}table{border-collapse:collapse;width:100%;font-size:12px}th,td{text-align:left;vertical-align:top;border-bottom:1px solid var(--line);padding:9px;white-space:nowrap}th{font-weight:600}pre{white-space:pre-wrap;overflow:auto;background:#fafafa;padding:12px}.hidden{display:none}@media(max-width:600px){.side{width:120px;padding:24px 12px}.app{margin-left:120px}.pane{flex-basis:330px;padding:28px 18px}.token{padding:18px;display:block}.token button{margin-top:10px}}
</style></head><body><aside class="side"><div class="brand">pibase</div><button class="menu" data-menu="projects">Projects</button><div id="projectMenus" class="hidden"><button class="menu secondary" data-menu="tables">Tables</button><button class="menu secondary" data-menu="users">Users</button><button class="menu secondary" data-menu="sql">SQL</button><button class="menu secondary" data-menu="migrations">Migrations</button><button class="menu secondary" data-menu="backups">Backups</button><button class="menu secondary" data-menu="extensions">Extensions</button></div></aside><main class="app"><div class="token"><input id="token" type="password" placeholder="Operator token" autocomplete="off"><button id="load">Load projects</button><span id="status" class="muted"></span></div><div id="chain" class="chain"></div></main><script>
const q=id=>document.getElementById(id), chain=q('chain');let projects=[],selected=null;
const auth=()=>({Authorization:'Bearer '+q('token').value,'Content-Type':'application/json'}), projectPath=()=> '/_pibase/projects/'+encodeURIComponent(selected.id);
function el(tag,text){const e=document.createElement(tag);if(text!==undefined)e.textContent=text;return e}function button(text,fn,cls){const b=el('button',text);if(cls)b.className=cls;b.onclick=fn;return b}function pane(title){const p=el('section');p.className='pane';p.append(el('h1',title));return p}function clearFrom(n){while(chain.children.length>n)chain.lastChild.remove()}function add(p){chain.append(p);chain.scrollLeft=chain.scrollWidth}function say(v){q('status').textContent=v}
async function api(path,opt={}){opt.headers=auth();const r=await fetch(path,opt),t=await r.text();if(!r.ok)throw Error(t||r.status);return t?JSON.parse(t):null}function body(v,method='POST'){return{method,body:JSON.stringify(v)}}
function projectsPane(){const p=pane('Projects');const create=el('div');create.className='group';create.append(el('div','Create project'));const input=el('input');input.placeholder='Project name';create.append(input,button('Create',async()=>{try{const x=await api('/_pibase/projects',body({name:input.value}));projects.push(x);renderProjects();clearFrom(1);const kp=pane(x.name);kp.append(el('p','Shown once; copy them now.'));const ak=el('p');ak.textContent='anon key: '+x.anon_key;const sk=el('p');sk.textContent='service key: '+x.service_key;kp.append(ak,sk);add(kp)}catch(e){say(e.message)}}));p.append(create);const list=el('div');list.id='projectList';p.append(list);return p}
function renderProjects(){const list=q('projectList');if(!list)return;list.replaceChildren();for(const x of projects){const b=button(x.name,()=>openProject(x),'item');b.append(el('span',x.id));list.append(b)}}
function openProject(x){selected=x;q('projectMenus').classList.remove('hidden');clearFrom(1);const p=pane(x.name);p.append(el('p',x.id));p.append(button('Tables',openTables),button('Users',openUsers),button('SQL',openSQL),button('Migrations',openMigrations),button('Backups',openBackups),button('Extensions',openExtensions));const danger=el('div');danger.className='group danger';danger.append(el('div','Delete project — type its id to confirm'));const i=el('input');danger.append(i,button('Delete project',async()=>{try{await api(projectPath(),body({confirm:i.value},'DELETE'));selected=null;q('projectMenus').classList.add('hidden');await load();}catch(e){say(e.message)}}));p.append(danger);add(p)}
async function openTables(){clearFrom(2);const p=pane('Tables');p.append(el('p','Loading…'));add(p);try{const ts=await api(projectPath()+'/tables');p.replaceChildren(el('h1','Tables'));for(const t of ts){const b=button(t.name,()=>openTable(t),'item');b.append(el('span',t.count+' rows · RLS '+(t.rls?'on':'off')+' · anon '+(t.anon_readable?'yes':'no')));p.append(b)}}catch(e){p.append(el('p',e.message))}}
function openTable(t){clearFrom(3);const p=pane(t.name);p.append(el('p',t.count+' rows · RLS '+(t.rls?'on':'off')+' · anon readable '+(t.anon_readable?'yes':'no')));p.append(button('Browse rows',()=>openRows(t)));add(p)}
async function openRows(t,offset=0){clearFrom(4);const p=pane(t.name+' rows');p.append(el('p','Loading…'));add(p);try{const data=await api(projectPath()+'/tables/'+encodeURIComponent(t.name)+'/rows?offset='+offset);p.replaceChildren(el('h1',t.name+' rows'));const d=el('div');d.className='data';const table=el('table'),head=el('tr');for(const c of data.columns)head.append(el('th',c));table.append(head);for(const row of data.rows){const tr=el('tr');for(const c of data.columns){const td=el('td');if(row[c]===null){td.textContent='NULL';td.style.color='var(--muted)'}else{td.textContent=row[c]}tr.append(td)}table.append(tr)}d.append(table);p.append(d);const nav=el('div');nav.className='row group';const prev=button('Previous',()=>openRows(t,Math.max(0,offset-50)));if(offset===0)prev.disabled=true;const next=button('Next',()=>openRows(t,offset+50));if(!data.has_more)next.disabled=true;nav.append(prev,next,el('span','Rows '+(offset+1)+'–'+(offset+data.rows.length)));p.append(nav)}catch(e){p.append(el('p',e.message))}}
async function openUsers(){clearFrom(2);const p=pane('Users');p.append(el('p','Loading…'));add(p);try{const users=await api(projectPath()+'/users');p.replaceChildren(el('h1','Users'));const email=el('input');email.placeholder='Email';const pass=el('input');pass.type='password';pass.placeholder='Password (8+ characters)';p.append(email,pass,button('Create user',async()=>{try{await api(projectPath()+'/users',body({email:email.value,password:pass.value,email_confirm:true}));openUsers()}catch(e){say(e.message)}}));for(const u of users){const g=el('div');g.className='group';g.append(el('div',u.email),el('div',u.id));const c=el('input');c.placeholder='Type id to confirm';g.append(c,button('Delete user',async()=>{try{await api(projectPath()+'/users/'+encodeURIComponent(u.id),body({confirm:c.value},'DELETE'));openUsers()}catch(e){say(e.message)}},'danger'));p.append(g)}}catch(e){p.append(el('p',e.message))}}
function simple(title,content){clearFrom(2);const p=pane(title);p.append(content);add(p)}function openSQL(){const a=el('textarea');const out=el('pre');const box=el('div');box.append(a,button('Run SQL',async()=>{try{out.textContent=JSON.stringify(await api(projectPath()+'/sql',body({sql:a.value})),null,2)}catch(e){out.textContent=e.message}}),out);simple('SQL',box)}async function openMigrations(){clearFrom(2);const p=pane('Migrations');const nameIn=el('input');nameIn.placeholder='Migration name';const sqlIn=el('textarea');sqlIn.placeholder='SQL';const status=el('p');status.className='muted';const g=el('div');g.className='group';g.append(el('div','Apply migration'),nameIn,sqlIn,button('Apply',async()=>{try{await api(projectPath()+'/migrations',body({name:nameIn.value,sql:sqlIn.value}));status.textContent='Applied: '+nameIn.value;openMigrations()}catch(e){status.textContent=e.message}}),status);p.append(g);p.append(el('h1','Applied'));try{const ms=await api(projectPath()+'/migrations');for(const m of ms){const d=el('div');d.className='item';d.textContent=m.name;d.append(el('span',m.checksum+' · '+m.applied_at));p.append(d)}if(!ms.length)p.append(el('p','No migrations applied.'))}catch(e){p.append(el('p',e.message))}add(p)}async function openBackups(){clearFrom(2);const p=pane('Backups');const status=el('p');status.className='muted';p.append(button('Back up now',async()=>{try{const r=await api(projectPath()+'/backup',{method:'POST'});status.textContent='Created: '+r.file;openBackups()}catch(e){status.textContent=e.message}}),status);try{const bs=await api(projectPath()+'/backups');for(const b of bs){const d=el('div');d.className='item';d.textContent=b.name;d.append(el('span',Math.round(b.size/1024)+' KB'));p.append(d)}if(!bs.length)p.append(el('p','No backups yet.'))}catch(e){p.append(el('p',e.message))}add(p)}function openExtensions(){const x=el('div');x.append(button('Enable PostGIS',()=>api(projectPath()+'/extensions/postgis',{method:'POST'}).then(()=>say('PostGIS enabled')).catch(e=>say(e.message))),button('Enable pgvector',()=>api(projectPath()+'/extensions/pgvector',{method:'POST'}).then(()=>say('pgvector enabled')).catch(e=>say(e.message))));simple('Extensions',x)}
async function load(){try{projects=await api('/_pibase/projects');selected=null;q('projectMenus').classList.add('hidden');clearFrom(0);add(projectsPane());renderProjects();say(projects.length+' projects loaded')}catch(e){say(e.message)}}q('load').onclick=load;document.querySelectorAll('[data-menu]').forEach(b=>b.onclick=()=>{if(b.dataset.menu==='projects')load();else if(selected)({tables:openTables,users:openUsers,sql:openSQL,migrations:openMigrations,backups:openBackups,extensions:openExtensions}[b.dataset.menu])()});add(projectsPane());
</script></body></html>`
