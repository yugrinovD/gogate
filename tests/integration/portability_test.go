//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/yugrinovD/gogate/internal/store"
)

func TestMigrationsWithoutPgvector(t *testing.T) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("gogate"),
		postgres.WithUsername("gogate"),
		postgres.WithPassword("gogate"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	conn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, conn, 5, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate without pgvector: %v", err)
	}
	if err := st.EnsureSemanticSchema(ctx); err != nil {
		t.Fatalf("ensure semantic schema without pgvector: %v", err)
	}
	hasVector, err := st.HasVector(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasVector {
		t.Fatal("plain postgres should not report pgvector")
	}
	var exists bool
	if err := st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='semantic_cache')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("semantic_cache table should be skipped without pgvector")
	}
}
