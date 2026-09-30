package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
)

type Project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Database   string `json:"database"`
	AnonKey    string `json:"anon_key"`
	ServiceKey string `json:"service_key"`
}

func CreateProject(ctx context.Context, adminURL, name string, masterKey []byte) (_ Project, err error) {
	if name == "" {
		return Project{}, fmt.Errorf("project name is required")
	}
	idBytes := make([]byte, 4)
	if _, err := rand.Read(idBytes); err != nil {
		return Project{}, err
	}
	id := hex.EncodeToString(idBytes)
	prefix := "p_" + id
	database := prefix
	anon, authenticated := prefix+"_anon", prefix+"_authenticated"
	service, admin := prefix+"_service_role", prefix+"_admin"

	root, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return Project{}, fmt.Errorf("connect admin database: %w", err)
	}
	defer root.Close(ctx)
	created := []string{}
	cleanup := func() {
		_, _ = root.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{database}.Sanitize()+` WITH (FORCE)`)
		for i := len(created) - 1; i >= 0; i-- {
			_, _ = root.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{created[i]}.Sanitize())
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	roles := []struct{ name, attrs string }{{anon, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE"}, {authenticated, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE"}, {service, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS"}, {admin, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE"}}
	for _, role := range roles {
		if _, err = root.Exec(ctx, `CREATE ROLE `+pgx.Identifier{role.name}.Sanitize()+` `+role.attrs); err != nil {
			return Project{}, err
		}
		created = append(created, role.name)
	}
	for _, role := range []string{anon, authenticated, service, admin} {
		if _, err = root.Exec(ctx, `GRANT `+pgx.Identifier{role}.Sanitize()+` TO authenticator`); err != nil {
			return Project{}, err
		}
	}
	if _, err = root.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{database}.Sanitize()+` OWNER `+pgx.Identifier{admin}.Sanitize()); err != nil {
		return Project{}, err
	}
	if _, err = root.Exec(ctx, `REVOKE CONNECT ON DATABASE `+pgx.Identifier{database}.Sanitize()+` FROM PUBLIC`); err != nil {
		return Project{}, err
	}
	for _, role := range []string{"authenticator", anon, authenticated, service, admin} {
		if _, err = root.Exec(ctx, `GRANT CONNECT ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO `+pgx.Identifier{role}.Sanitize()); err != nil {
			return Project{}, err
		}
	}

	projectURL, err := withDatabase(adminURL, database)
	if err != nil {
		return Project{}, err
	}
	projectDB, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		return Project{}, err
	}
	setup := `REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA auth AUTHORIZATION ` + pgx.Identifier{admin}.Sanitize() + `;
CREATE SCHEMA storage AUTHORIZATION ` + pgx.Identifier{admin}.Sanitize() + `;
REVOKE ALL ON SCHEMA auth, storage FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + `, ` + pgx.Identifier{service}.Sanitize() + `;`
	_, err = projectDB.Exec(ctx, setup)
	projectDB.Close(ctx)
	if err != nil {
		return Project{}, err
	}

	jwtSecret := make([]byte, 32)
	if _, err = rand.Read(jwtSecret); err != nil {
		return Project{}, err
	}
	anonKey, err := keys.JWT(jwtSecret, "anon")
	if err != nil {
		return Project{}, err
	}
	serviceKey, err := keys.JWT(jwtSecret, "service_role")
	if err != nil {
		return Project{}, err
	}
	encrypted, err := secretbox.Encrypt(masterKey, jwtSecret)
	if err != nil {
		return Project{}, err
	}

	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return Project{}, err
	}
	controlDB, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return Project{}, err
	}
	defer controlDB.Close(ctx)
	_, err = controlDB.Exec(ctx, `INSERT INTO control.projects
 (id,name,database_name,anon_role,authenticated_role,service_role,admin_role,jwt_secret_encrypted,anon_key_hash,service_key_hash)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, name, database, anon, authenticated, service, admin, encrypted, keys.Hash(anonKey), keys.Hash(serviceKey))
	if err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: name, Database: database, AnonKey: anonKey, ServiceKey: serviceKey}, nil
}

func JSON(project Project) ([]byte, error) { return json.MarshalIndent(project, "", "  ") }

func DeleteProject(ctx context.Context, adminURL, id string) error {
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return err
	}
	controlDB, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return err
	}
	var database string
	var roles []string
	err = controlDB.QueryRow(ctx, `SELECT database_name,ARRAY[anon_role,authenticated_role,service_role,admin_role] FROM control.projects WHERE id=$1`, id).Scan(&database, &roles)
	controlDB.Close(ctx)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("project %q not found", id)
		}
		return err
	}
	root, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer root.Close(ctx)
	if _, err := root.Exec(ctx, `DROP DATABASE `+pgx.Identifier{database}.Sanitize()+` WITH (FORCE)`); err != nil {
		return err
	}
	controlDB, err = pgx.Connect(ctx, controlURL)
	if err != nil {
		return err
	}
	if _, err := controlDB.Exec(ctx, `DELETE FROM control.projects WHERE id=$1`, id); err != nil {
		controlDB.Close(ctx)
		return err
	}
	controlDB.Close(ctx)
	for _, role := range roles {
		if _, err := root.Exec(ctx, `DROP ROLE `+pgx.Identifier{role}.Sanitize()); err != nil {
			return err
		}
	}
	return nil
}
