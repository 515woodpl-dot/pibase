package control

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/jackc/pgx/v5"
)

// BranchProject creates fresh roles and keys, then clones only the source
// project's public schema and data. Auth users, sessions, storage metadata and
// object files are deliberately excluded so a development branch cannot copy
// production identities or private files by accident.
func BranchProject(ctx context.Context, adminURL, sourceID, name string, masterKey []byte) (_ Project, err error) {
	source, err := branchInfo(ctx, adminURL, sourceID)
	if err != nil {
		return Project{}, err
	}
	target, err := CreateProject(ctx, adminURL, name, masterKey)
	if err != nil {
		return Project{}, err
	}
	defer func() {
		if err != nil {
			_ = DeleteProject(context.Background(), adminURL, target.ID)
		}
	}()
	targetInfo, err := branchInfo(ctx, adminURL, target.ID)
	if err != nil {
		return Project{}, err
	}
	extensions, err := optionalProjectExtensions(ctx, withDatabaseMust(adminURL, source.database))
	if err != nil {
		return Project{}, fmt.Errorf("read branch extensions: %w", err)
	}
	for _, extension := range extensions {
		switch extension {
		case "postgis":
			err = EnablePostGIS(ctx, adminURL, target.ID)
		case "vector":
			err = EnablePGVector(ctx, adminURL, target.ID)
		}
		if err != nil {
			return Project{}, fmt.Errorf("enable branch extension %s: %w", extension, err)
		}
	}

	archive, err := os.CreateTemp("", "pibase-branch-*.dump")
	if err != nil {
		return Project{}, err
	}
	archivePath := archive.Name()
	if err = archive.Close(); err != nil {
		return Project{}, err
	}
	defer os.Remove(archivePath)
	list, err := os.CreateTemp("", "pibase-branch-*.toc")
	if err != nil {
		return Project{}, err
	}
	listPath := list.Name()
	defer os.Remove(listPath)

	dumpArgs := []string{"--format=custom", "--schema=public", "--no-owner", "--no-acl", "--file=" + archivePath}
	for _, extension := range extensions {
		if extension == "postgis" {
			// PostGIS marks spatial_ref_sys as extension-managed configuration
			// data. The fresh target extension already supplies it.
			dumpArgs = append(dumpArgs, "--exclude-table=public.spatial_ref_sys")
		}
	}
	dumpArgs = append(dumpArgs, withDatabaseMust(adminURL, source.database))
	if output, dumpErr := exec.CommandContext(ctx, "pg_dump", dumpArgs...).CombinedOutput(); dumpErr != nil {
		return Project{}, fmt.Errorf("pg_dump branch source: %w: %s", dumpErr, output)
	}
	listCommand := exec.CommandContext(ctx, "pg_restore", "--list", archivePath)
	toc, err := listCommand.StdoutPipe()
	if err != nil {
		return Project{}, err
	}
	var listErr strings.Builder
	listCommand.Stderr = &listErr
	if err = listCommand.Start(); err != nil {
		return Project{}, err
	}
	filterErr := filterPublicSchemaTOC(list, toc)
	closeErr := list.Close()
	waitErr := listCommand.Wait()
	if filterErr != nil {
		return Project{}, filterErr
	}
	if closeErr != nil {
		return Project{}, closeErr
	}
	if waitErr != nil {
		return Project{}, fmt.Errorf("list branch archive: %w: %s", waitErr, listErr.String())
	}
	restoreArgs := []string{"--exit-on-error", "--no-owner", "--no-acl", "--role=" + targetInfo.admin, "--use-list=" + listPath, "--dbname=" + withDatabaseMust(adminURL, targetInfo.database), archivePath}
	if output, restoreErr := exec.CommandContext(ctx, "pg_restore", restoreArgs...).CombinedOutput(); restoreErr != nil {
		return Project{}, fmt.Errorf("pg_restore branch target: %w: %s", restoreErr, output)
	}
	if err = remapBranchSecurity(ctx, withDatabaseMust(adminURL, source.database), withDatabaseMust(adminURL, targetInfo.database), source, targetInfo); err != nil {
		return Project{}, fmt.Errorf("remap branch security: %w", err)
	}
	return target, nil
}

func filterPublicSchemaTOC(dst io.Writer, src io.Reader) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	omitted := 0
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 7 && strings.HasSuffix(fields[0], ";") && fields[3] == "SCHEMA" && fields[5] == "public" {
			omitted++
			continue
		}
		if _, err := fmt.Fprintln(dst, line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if omitted != 1 {
		return fmt.Errorf("branch archive contains %d public schema entries, want exactly 1", omitted)
	}
	return nil
}

type branchProjectInfo struct{ database, anon, authenticated, service, admin string }

func optionalProjectExtensions(ctx context.Context, projectURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT extname FROM pg_extension ORDER BY extname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var extensions []string
	for rows.Next() {
		var extension string
		if err := rows.Scan(&extension); err != nil {
			return nil, err
		}
		switch extension {
		case "plpgsql":
			continue
		case "postgis", "vector":
			extensions = append(extensions, extension)
		default:
			return nil, fmt.Errorf("branching projects with extension %q is not supported; only postgis and vector are preserved", extension)
		}
	}
	return extensions, rows.Err()
}

func branchInfo(ctx context.Context, adminURL, id string) (branchProjectInfo, error) {
	controlURL, e := withDatabase(adminURL, "_control")
	if e != nil {
		return branchProjectInfo{}, e
	}
	conn, e := pgx.Connect(ctx, controlURL)
	if e != nil {
		return branchProjectInfo{}, e
	}
	defer conn.Close(ctx)
	var p branchProjectInfo
	e = conn.QueryRow(ctx, `SELECT database_name,anon_role,authenticated_role,service_role,admin_role FROM control.projects WHERE id=$1`, id).Scan(&p.database, &p.anon, &p.authenticated, &p.service, &p.admin)
	if e == pgx.ErrNoRows {
		return p, fmt.Errorf("project %q not found", id)
	}
	return p, e
}
func withDatabaseMust(raw, database string) string {
	value, err := withDatabase(raw, database)
	if err != nil {
		return raw
	}
	return value
}

type branchGrant struct {
	kind, schema, object, column, args, privilege, role, owner, relkind string
	grantable                                                           bool
}

type branchPolicy struct {
	schema, table, name string
	roles               []string
}

var branchRolePrivileges = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"TRUNCATE": true, "REFERENCES": true, "TRIGGER": true,
	"USAGE": true, "CREATE": true, "CONNECT": true, "EXECUTE": true,
}

func remapBranchSecurity(ctx context.Context, sourceURL, targetURL string, source, target branchProjectInfo) error {
	grants, policies, defaults, err := readBranchSecurity(ctx, sourceURL, source)
	if err != nil {
		return err
	}
	targetConn, err := pgx.Connect(ctx, targetURL)
	if err != nil {
		return err
	}
	defer targetConn.Close(ctx)
	tx, err := targetConn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	roles := []string{target.anon, target.authenticated, target.service, target.admin}
	for _, role := range append(roles, "PUBLIC") {
		roleSQL := role
		if role != "PUBLIC" {
			roleSQL = pgx.Identifier{role}.Sanitize()
		}
		if _, err = tx.Exec(ctx, `REVOKE ALL ON SCHEMA public FROM `+roleSQL); err != nil {
			return err
		}
	}
	for _, grant := range grants {
		role, err := mapBranchRole(grant.role, source, target)
		if err != nil {
			return err
		}
		if err = applyBranchGrant(ctx, tx, grant, role); err != nil {
			return err
		}
	}
	for _, policy := range policies {
		mapped := make([]string, 0, len(policy.roles))
		for _, role := range policy.roles {
			mappedRole, err := mapBranchRole(role, source, target)
			if err != nil {
				return err
			}
			mapped = append(mapped, mappedRole)
		}
		if len(mapped) == 0 {
			return fmt.Errorf("policy %s.%s.%s has no supported roles", policy.schema, policy.table, policy.name)
		}
		roleSQL := make([]string, len(mapped))
		for i, role := range mapped {
			if role == "PUBLIC" {
				roleSQL[i] = "PUBLIC"
			} else {
				roleSQL[i] = pgx.Identifier{role}.Sanitize()
			}
		}
		statement := `ALTER POLICY ` + pgx.Identifier{policy.name}.Sanitize() + ` ON ` + pgx.Identifier{policy.schema, policy.table}.Sanitize() + ` TO ` + strings.Join(roleSQL, ",")
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	for _, grant := range defaults {
		role, err := mapBranchRole(grant.role, source, target)
		if err != nil {
			return err
		}
		objectType, ok := mapDefaultObjectType(grant.object)
		if !ok {
			return fmt.Errorf("unsupported default privilege object type %q", grant.object)
		}
		if !branchRolePrivileges[grant.privilege] {
			return fmt.Errorf("unsupported privilege %q", grant.privilege)
		}
		owner, err := mapBranchRole(grant.owner, source, target)
		if err != nil {
			return err
		}
		statement := `ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{owner}.Sanitize()
		if grant.schema != "" {
			statement += ` IN SCHEMA ` + pgx.Identifier{grant.schema}.Sanitize()
		}
		roleSQL := role
		if role != "PUBLIC" {
			roleSQL = pgx.Identifier{role}.Sanitize()
		}
		statement += ` GRANT ` + grant.privilege + ` ON ` + objectType + ` TO ` + roleSQL
		if grant.grantable {
			statement += ` WITH GRANT OPTION`
		}
		if _, err = tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func readBranchSecurity(ctx context.Context, sourceURL string, source branchProjectInfo) ([]branchGrant, []branchPolicy, []branchGrant, error) {
	conn, err := pgx.Connect(ctx, sourceURL)
	if err != nil {
		return nil, nil, nil, err
	}
	defer conn.Close(ctx)
	var grants []branchGrant
	queries := []struct {
		kind, sql string
	}{
		{"schema", `SELECT n.nspname,CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE r.rolname END,x.privilege_type,x.is_grantable FROM pg_namespace n CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n'::"char",n.nspowner))) x LEFT JOIN pg_roles r ON r.oid=x.grantee WHERE n.nspname='public'`},
		{"relation", `SELECT n.nspname,c.relname,c.relkind::text,'',CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE r.rolname END,x.privilege_type,x.is_grantable FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) x LEFT JOIN pg_roles r ON r.oid=x.grantee WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f','S') AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')`},
		{"column", `SELECT n.nspname,c.relname,a.attname,CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE r.rolname END,x.privilege_type,x.is_grantable FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(a.attacl) x LEFT JOIN pg_roles r ON r.oid=x.grantee WHERE n.nspname='public' AND a.attnum>0 AND NOT a.attisdropped AND a.attacl IS NOT NULL AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')`},
		{"function", `SELECT n.nspname,p.proname,pg_get_function_identity_arguments(p.oid),CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE r.rolname END,x.privilege_type,x.is_grantable FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl,acldefault('f'::"char",p.proowner))) x LEFT JOIN pg_roles r ON r.oid=x.grantee WHERE n.nspname='public' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')`},
	}
	for _, query := range queries {
		rows, err := conn.Query(ctx, query.sql)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read %s grants: %w", query.kind, err)
		}
		for rows.Next() {
			var grant branchGrant
			grant.kind = query.kind
			switch query.kind {
			case "schema":
				err = rows.Scan(&grant.schema, &grant.role, &grant.privilege, &grant.grantable)
			case "relation":
				err = rows.Scan(&grant.schema, &grant.object, &grant.relkind, &grant.column, &grant.role, &grant.privilege, &grant.grantable)
			case "column":
				err = rows.Scan(&grant.schema, &grant.object, &grant.column, &grant.role, &grant.privilege, &grant.grantable)
			case "function":
				err = rows.Scan(&grant.schema, &grant.object, &grant.args, &grant.role, &grant.privilege, &grant.grantable)
			}
			if err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
			grants = append(grants, grant)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		rows.Close()
	}
	policies, err := readBranchPolicies(ctx, conn)
	if err != nil {
		return nil, nil, nil, err
	}
	defaults, err := readBranchDefaults(ctx, conn)
	if err != nil {
		return nil, nil, nil, err
	}
	_ = source
	return grants, policies, defaults, nil
}

func readBranchPolicies(ctx context.Context, conn *pgx.Conn) ([]branchPolicy, error) {
	rows, err := conn.Query(ctx, `SELECT n.nspname,c.relname,p.polname,CASE WHEN role_oid=0 THEN 'PUBLIC' ELSE r.rolname END FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL unnest(p.polroles) role_oid LEFT JOIN pg_roles r ON r.oid=role_oid WHERE n.nspname='public' ORDER BY n.nspname,c.relname,p.polname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := make(map[string]*branchPolicy)
	var order []string
	for rows.Next() {
		var schema, table, name, role string
		if err := rows.Scan(&schema, &table, &name, &role); err != nil {
			return nil, err
		}
		key := schema + "\x00" + table + "\x00" + name
		policy := byName[key]
		if policy == nil {
			policy = &branchPolicy{schema: schema, table: table, name: name}
			byName[key] = policy
			order = append(order, key)
		}
		policy.roles = append(policy.roles, role)
	}
	result := make([]branchPolicy, 0, len(order))
	for _, key := range order {
		result = append(result, *byName[key])
	}
	return result, rows.Err()
}

func readBranchDefaults(ctx context.Context, conn *pgx.Conn) ([]branchGrant, error) {
	rows, err := conn.Query(ctx, `SELECT COALESCE(n.nspname,''),d.defaclobjtype::text,CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE r.rolname END,x.privilege_type,x.is_grantable,o.rolname FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) x LEFT JOIN pg_roles r ON r.oid=x.grantee JOIN pg_roles o ON o.oid=d.defaclrole WHERE n.nspname='public'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []branchGrant
	for rows.Next() {
		var grant branchGrant
		if err := rows.Scan(&grant.schema, &grant.object, &grant.role, &grant.privilege, &grant.grantable, &grant.owner); err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, rows.Err()
}

func mapBranchRole(role string, source, target branchProjectInfo) (string, error) {
	if role == "PUBLIC" || role == "pg_database_owner" {
		return role, nil
	}
	roles := map[string]string{source.anon: target.anon, source.authenticated: target.authenticated, source.service: target.service, source.admin: target.admin}
	if mapped, ok := roles[role]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("branch security references unmappable role %q", role)
}

func applyBranchGrant(ctx context.Context, tx pgx.Tx, grant branchGrant, role string) error {
	if !branchRolePrivileges[grant.privilege] {
		return fmt.Errorf("unsupported privilege %q", grant.privilege)
	}
	if role == "PUBLIC" {
		role = "PUBLIC"
	} else {
		role = pgx.Identifier{role}.Sanitize()
	}
	privilege := grant.privilege
	var object string
	switch grant.kind {
	case "schema":
		object = `SCHEMA ` + pgx.Identifier{grant.schema}.Sanitize()
	case "relation":
		objectType := "TABLE"
		if grant.relkind == "S" {
			objectType = "SEQUENCE"
		}
		object = objectType + ` ` + pgx.Identifier{grant.schema, grant.object}.Sanitize()
		if grant.column != "" {
			return fmt.Errorf("relation grant unexpectedly has column %q", grant.column)
		}
	case "column":
		object = `TABLE ` + pgx.Identifier{grant.schema, grant.object}.Sanitize() + ` (` + pgx.Identifier{grant.column}.Sanitize() + `)`
	case "function":
		object = `FUNCTION ` + pgx.Identifier{grant.schema, grant.object}.Sanitize() + `(` + grant.args + `)`
	default:
		return fmt.Errorf("unsupported grant kind %q", grant.kind)
	}
	statement := `GRANT ` + privilege + ` ON ` + object + ` TO ` + role
	if grant.grantable {
		statement += ` WITH GRANT OPTION`
	}
	_, err := tx.Exec(ctx, statement)
	return err
}

func mapDefaultObjectType(value string) (string, bool) {
	switch value {
	case "r":
		return "TABLES", true
	case "S":
		return "SEQUENCES", true
	case "f":
		return "FUNCTIONS", true
	case "T":
		return "TYPES", true
	case "n":
		return "SCHEMAS", true
	default:
		return "", false
	}
}
