package control

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

	dump := exec.CommandContext(ctx, "pg_dump", "--schema=public", "--clean", "--if-exists", "--no-owner", withDatabaseMust(adminURL, source.database))
	stdout, err := dump.StdoutPipe()
	if err != nil {
		return Project{}, err
	}
	var dumpErr strings.Builder
	dump.Stderr = &dumpErr
	psql := exec.CommandContext(ctx, "psql", "--set", "ON_ERROR_STOP=1", "--dbname", withDatabaseMust(adminURL, targetInfo.database))
	stdin, err := psql.StdinPipe()
	if err != nil {
		return Project{}, err
	}
	var psqlErr strings.Builder
	psql.Stderr = &psqlErr
	if err = psql.Start(); err != nil {
		return Project{}, err
	}
	if err = dump.Start(); err != nil {
		return Project{}, err
	}
	_, _ = fmt.Fprintf(stdin, "SET ROLE %s;\n", pgx.Identifier{targetInfo.admin}.Sanitize())
	replacer := strings.NewReplacer(source.anon, targetInfo.anon, source.authenticated, targetInfo.authenticated, source.service, targetInfo.service, source.admin, targetInfo.admin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		upper := strings.ToUpper(line)
		if (strings.Contains(upper, " POLICY ") && strings.Contains(upper, " TO ")) || strings.HasPrefix(upper, "GRANT ") || strings.HasPrefix(upper, "REVOKE ") {
			line = replacer.Replace(line)
		}
		if _, err = io.WriteString(stdin, line+"\n"); err != nil {
			break
		}
	}
	if scanErr := scanner.Err(); err == nil {
		err = scanErr
	}
	_ = stdin.Close()
	dumpWait := dump.Wait()
	psqlWait := psql.Wait()
	if err != nil {
		return Project{}, err
	}
	if dumpWait != nil {
		return Project{}, fmt.Errorf("pg_dump branch source: %w: %s", dumpWait, dumpErr.String())
	}
	if psqlWait != nil {
		return Project{}, fmt.Errorf("psql branch target: %w: %s", psqlWait, psqlErr.String())
	}
	return target, nil
}

type branchProjectInfo struct{ database, anon, authenticated, service, admin string }

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
