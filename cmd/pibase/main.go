package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/config"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/gateway"
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
		return errors.New("usage: pibase <bootstrap|project-create|project-delete|migration-apply|sql|serve>")
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
	case "serve":
		return serve()
	case "migration-apply":
		if len(os.Args) != 4 {
			return errors.New("usage: pibase migration-apply <project-id> <name> < migration.sql")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
		if err != nil {
			return err
		}
		return adminsql.ApplyMigration(ctx, url, os.Args[2], os.Args[3], string(input))
	case "sql":
		if len(os.Args) != 3 {
			return errors.New("usage: pibase sql <project-id> < statement.sql")
		}
		url, err := config.Required("PIBASE_ADMIN_DATABASE_URL")
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
