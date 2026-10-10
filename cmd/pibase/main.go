package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminserver"
	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/backup"
	"github.com/515woodpl-dot/pibase/internal/config"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/gateway"
	"github.com/515woodpl-dot/pibase/internal/mailer"
	"github.com/515woodpl-dot/pibase/internal/secretbox"
)

func main() {
	if err := run(); err != nil {
		slog.Error("pibase stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: pibase <bootstrap|project-create|project-branch|project-delete|project-list|project-usage|pgvector-enable|postgis-enable|backup|restore|migration-apply|sql|serve|admin-serve>")
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "bootstrap":
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		password, err := config.Required("PIBASE_AUTHENTICATOR_PASSWORD")
		if err != nil {
			return err
		}
		return control.Bootstrap(ctx, url, password)
	case "project-create":
		if len(os.Args) != 3 {
			return errors.New("usage: pibase project-create <name>")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		encodedKey, err := config.Required("PIBASE_MASTER_KEY")
		if err != nil {
			return err
		}
		key, err := secretbox.DecodeKey(encodedKey)
		if err != nil {
			return err
		}
		project, err := control.CreateProject(ctx, url, os.Args[2], key)
		if err != nil {
			return err
		}
		output, err := control.JSON(project)
		if err != nil {
			return err
		}
		fmt.Println(string(output))
		return nil
	case "project-delete":
		if len(os.Args) != 3 {
			return errors.New("usage: pibase project-delete <project-id>")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		return control.DeleteProject(ctx, url, os.Args[2])
	case "project-branch":
		if len(os.Args) != 4 {
			return errors.New("usage: pibase project-branch <source-project-id> <new-name>")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		encodedKey, err := config.Required("PIBASE_MASTER_KEY")
		if err != nil {
			return err
		}
		key, err := secretbox.DecodeKey(encodedKey)
		if err != nil {
			return err
		}
		project, err := control.BranchProject(ctx, url, os.Args[2], os.Args[3], key)
		if err != nil {
			return err
		}
		output, err := control.JSON(project)
		if err != nil {
			return err
		}
		fmt.Println(string(output))
		return nil
	case "project-list":
		if len(os.Args) != 2 {
			return errors.New("usage: pibase project-list")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		projects, err := control.ListProjects(ctx, url)
		if err != nil {
			return err
		}
		output, err := json.MarshalIndent(projects, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(output))
		return nil
	case "project-usage":
		if len(os.Args) != 3 {
			return errors.New("usage: pibase project-usage <project-id>")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		usage, err := control.Usage(ctx, url, os.Args[2], time.Time{}, time.Time{})
		if err != nil {
			return err
		}
		output, err := json.MarshalIndent(usage, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(output))
		return nil
	case "pgvector-enable":
		if len(os.Args) != 3 {
			return errors.New("usage: pibase pgvector-enable <project-id>")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		return control.EnablePGVector(ctx, url, os.Args[2])
	case "postgis-enable":
		if len(os.Args) != 3 {
			return errors.New("usage: pibase postgis-enable <project-id>")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		return control.EnablePostGIS(ctx, url, os.Args[2])
	case "backup", "restore":
		if len(os.Args) != 4 {
			return fmt.Errorf("usage: pibase %s <project-id> <destination-or-backup>", os.Args[1])
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		encodedKey, err := config.Required("PIBASE_BACKUP_KEY")
		if err != nil {
			return err
		}
		keyBytes, err := secretbox.DecodeKey(encodedKey)
		if err != nil {
			return fmt.Errorf("decode backup key: %w", err)
		}
		var key [32]byte
		copy(key[:], keyBytes)
		storageRoot := os.Getenv("PIBASE_STORAGE_ROOT")
		if storageRoot == "" {
			storageRoot = "/data/files"
		}
		if os.Args[1] == "backup" {
			return backup.Backup(ctx, url, os.Args[2], storageRoot, os.Args[3], key)
		}
		if os.Getenv("PIBASE_RESTORE_CONFIRM") != os.Args[2] {
			return errors.New("restore is destructive; set PIBASE_RESTORE_CONFIRM to the project id")
		}
		return backup.Restore(ctx, url, os.Args[2], storageRoot, os.Args[3], key)
	case "serve":
		return serve()
	case "admin-serve":
		return adminServe()
	case "migration-apply":
		if len(os.Args) != 4 {
			return errors.New("usage: pibase migration-apply <project-id> <name> < migration.sql")
		}
		url, err := config.Required("PIBASE_DATABASE_URL")
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
		if err != nil {
			return err
		}
		return adminsql.ApplyMigration(ctx, url, os.Args[2], os.Args[3], string(input))
	case "project-export":
		// Writes a passphrase-encrypted file that another PIBASE server can
		// import. The passphrase comes from standard input.
		if len(os.Args) != 4 {
			return errors.New("usage: pibase project-export <project-id> <file> < passphrase")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		master, err := masterKeyFromEnv()
		if err != nil {
			return err
		}
		passphrase, err := passphraseFromStdin()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(os.Args[3], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := backup.ExportProject(ctx, url, os.Args[2], storageRootFromEnv(), master, passphrase, out); err != nil {
			out.Close()
			os.Remove(os.Args[3])
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		fmt.Println(os.Args[3])
		return nil
	case "project-import":
		// Creates a project from an export file, as the operator (database
		// extensions and private server-side code addresses are allowed).
		if len(os.Args) != 3 && len(os.Args) != 4 {
			return errors.New("usage: pibase project-import <file> [owner-email] < passphrase")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		master, err := masterKeyFromEnv()
		if err != nil {
			return err
		}
		passphrase, err := passphraseFromStdin()
		if err != nil {
			return err
		}
		opts := backup.ImportOptions{Operator: true}
		if len(os.Args) == 4 {
			if opts.OwnerAccountID, err = control.AccountIDByEmail(ctx, url, os.Args[3]); err != nil {
				return fmt.Errorf("no dashboard account with the email %s", os.Args[3])
			}
		}
		result, err := backup.ImportProject(ctx, url, storageRootFromEnv(), master, os.Args[2], passphrase, opts)
		if err != nil {
			return err
		}
		fmt.Printf("Imported %s (%s).\n", result.Name, result.ProjectID)
		for _, n := range result.Notes {
			fmt.Println("Note: " + n)
		}
		return nil
	case "account-create":
		// Creates a dashboard account. The password comes from standard input.
		if len(os.Args) != 4 {
			return errors.New("usage: pibase account-create <email> <organization> < password")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 2048))
		if err != nil {
			return err
		}
		password := strings.TrimRight(string(input), "\r\n")
		account, err := control.CreateAccount(ctx, url, os.Args[2], password, os.Args[3])
		if err != nil {
			if errors.Is(err, control.ErrDuplicate) {
				return fmt.Errorf("a dashboard account with that email already exists; change its password with account-password-reset")
			}
			return err
		}
		fmt.Printf("Created dashboard account %s.\n", account.Email)
		return nil
	case "account-password-reset":
		// The new password comes from standard input so it never shows up in
		// the process list or shell history.
		if len(os.Args) != 3 {
			return errors.New("usage: pibase account-password-reset <email> < new-password")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 2048))
		if err != nil {
			return err
		}
		password := strings.TrimRight(string(input), "\r\n")
		if err := control.SetAccountPassword(ctx, url, os.Args[2], password); err != nil {
			if errors.Is(err, control.ErrNotFound) {
				return fmt.Errorf("no dashboard account with that email")
			}
			return err
		}
		fmt.Println("Password changed; the account was signed out everywhere.")
		return nil
	case "sql":
		if len(os.Args) != 3 {
			return errors.New("usage: pibase sql <project-id> < statement.sql")
		}
		url, err := config.Required("PIBASE_DATABASE_URL")
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
		if err != nil {
			return err
		}
		result, err := adminsql.Run(ctx, url, os.Args[2], string(input))
		if err != nil {
			return err
		}
		output, err := adminsql.MarshalResult(result)
		if err != nil {
			return err
		}
		fmt.Println(string(output))
		return nil
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func adminServe() error {
	adminURL, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
	if err != nil {
		return err
	}
	databaseURL, err := config.Required("PIBASE_DATABASE_URL")
	if err != nil {
		return err
	}
	token, err := config.Required("PIBASE_OPERATOR_TOKEN")
	if err != nil {
		return err
	}
	masterRaw, err := config.Required("PIBASE_MASTER_KEY")
	if err != nil {
		return err
	}
	backupRaw, err := config.Required("PIBASE_BACKUP_KEY")
	if err != nil {
		return err
	}
	master, err := secretbox.DecodeKey(masterRaw)
	if err != nil {
		return err
	}
	backupKey, err := secretbox.DecodeKey(backupRaw)
	if err != nil {
		return err
	}
	addr := os.Getenv("PIBASE_ADMIN_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18081"
	}
	if err := validateAdminAddr(addr, os.Getenv("PIBASE_ADMIN_ALLOW_NONLOCAL") == "1"); err != nil {
		return err
	}
	maxProjects, err := config.Int("PIBASE_MAX_PROJECTS_PER_ACCOUNT", 2)
	if err != nil || maxProjects < 1 {
		return fmt.Errorf("PIBASE_MAX_PROJECTS_PER_ACCOUNT must be a positive integer")
	}
	trusted, err := adminserver.ParseTrustedProxies(os.Getenv("PIBASE_TRUSTED_PROXIES"))
	if err != nil {
		return err
	}
	// Reset emails need an SMTP server and the dashboard's own address; without
	// both, "Forgot password?" sends people to the operator instead.
	var sendMail func(to, subject, body string) error
	if m := mailer.FromEnv(); m.Configured() {
		sendMail = m.Send
	}
	dashboardURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PIBASE_DASHBOARD_URL")), "/")
	if dashboardURL != "" {
		if u, err := url.Parse(dashboardURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("PIBASE_DASHBOARD_URL must look like https://dashboard.example.com")
		}
	}
	s, err := adminserver.New(adminserver.Config{AdminURL: adminURL, DatabaseURL: databaseURL, StorageRoot: os.Getenv("PIBASE_STORAGE_ROOT"), BackupRoot: os.Getenv("PIBASE_BACKUP_ROOT"), Token: token, MasterKey: master, BackupKey: backupKey, Env: os.Getenv("PIBASE_ADMIN_ENV"), PublicURL: os.Getenv("PIBASE_PUBLIC_URL"), MaxProjectsPerAccount: maxProjects, TrustedProxies: trusted, SendMail: sendMail, DashboardURL: dashboardURL})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(c)
	}()
	slog.Info("operator dashboard listening", "address", addr)
	err = h.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func validateAdminAddr(addr string, allowNonLocal bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid PIBASE_ADMIN_LISTEN_ADDR: %w", err)
	}
	ip := net.ParseIP(host)
	if !allowNonLocal && (ip == nil || !ip.IsLoopback()) {
		return errors.New("admin listener must be loopback; set PIBASE_ADMIN_ALLOW_NONLOCAL=1 to override")
	}
	return nil
}

func serve() error {
	url, err := config.Required("PIBASE_DATABASE_URL")
	if err != nil {
		return err
	}
	encodedKey, err := config.Required("PIBASE_MASTER_KEY")
	if err != nil {
		return err
	}
	masterKey, err := secretbox.DecodeKey(encodedKey)
	if err != nil {
		return err
	}
	addr := os.Getenv("PIBASE_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	active, err := config.Int("PIBASE_M0_ACTIVE_PROJECTS", 0)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server, err := gateway.New(ctx, url, masterKey, active, os.Getenv("PIBASE_M0_LIGHT_LOAD") == "true")
	if err != nil {
		return err
	}
	defer server.Close()
	httpServer := &http.Server{Addr: addr, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	slog.Info("gateway listening", "address", addr, "active_projects", active)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func masterKeyFromEnv() ([]byte, error) {
	raw, err := config.Required("PIBASE_MASTER_KEY")
	if err != nil {
		return nil, err
	}
	return secretbox.DecodeKey(raw)
}

func storageRootFromEnv() string {
	if root := os.Getenv("PIBASE_STORAGE_ROOT"); root != "" {
		return root
	}
	return "/data/files"
}

func passphraseFromStdin() (string, error) {
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(input), "\r\n"), nil
}
