package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// InsertSemantic stores a response together with its embedding.
func (s *Store) InsertSemantic(ctx context.Context, tenantID, model string, promptHash []byte, providerName string, embedding []float32, response, usage json.RawMessage, cost float64, expiresAt time.Time) error {
	if len(embedding) != 768 {
		return fmt.Errorf("embedding must have 768 dimensions, got %d", len(embedding))
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO semantic_cache(tenant_id, model, prompt_hash, provider, embedding, response, usage, cost_usd, expires_at)
		VALUES($1,$2,$3,$4,$5::vector,$6,$7,$8,$9)`,
		tenantID, model, promptHash, providerName, encodeVector(embedding), []byte(response), []byte(usage), cost, expiresAt)
	return err
}

// FindSemantic returns the closest non-expired response above the similarity
// threshold. maxDistance is expressed as cosine distance (1 - similarity).
func (s *Store) FindSemantic(ctx context.Context, tenantID, model string, embedding []float32, maxDistance float64) (SemanticEntry, error) {
	var e SemanticEntry
	var response, usage []byte
	if len(embedding) != 768 {
		return e, fmt.Errorf("embedding must have 768 dimensions, got %d", len(embedding))
	}
	vec := encodeVector(embedding)
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, model, prompt_hash, provider, response, usage, cost_usd::float8, expires_at,
		       (embedding <=> $3::vector) AS distance
		FROM semantic_cache
		WHERE tenant_id=$1 AND model=$2 AND expires_at > now()
		  AND (embedding <=> $3::vector) <= $4
		ORDER BY embedding <=> $3::vector
		LIMIT 1`,
		tenantID, model, vec, maxDistance).
		Scan(&e.ID, &e.TenantID, &e.Model, &e.PromptHash, &e.Provider, &response, &usage, &e.CostUSD, &e.ExpiresAt, &e.Distance)
	if err != nil {
		return e, err
	}
	e.Response = json.RawMessage(response)
	e.Usage = json.RawMessage(usage)
	return e, nil
}

// DeleteSemantic removes a single semantic cache entry.
func (s *Store) DeleteSemantic(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM semantic_cache WHERE id=$1`, id)
	return err
}

func encodeVector(v []float32) string {
	b := make([]byte, 0, len(v)*12+2)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = fmt.Appendf(b, "%g", f)
	}
	b = append(b, ']')
	return string(b)
}

// HasVector reports whether the pgvector extension is installed.
func (s *Store) HasVector(ctx context.Context) (bool, error) {
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&ok)
	return ok, err
}

// EnsureSemanticSchema creates the semantic cache table if pgvector is
// available. It is safe to call after migrations and handles the case where the
// extension was installed after the initial schema migration.
func (s *Store) EnsureSemanticSchema(ctx context.Context) error {
	ok, err := s.HasVector(ctx)
	if err != nil || !ok {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
DO $do$
BEGIN
    EXECUTE $ddl$
        CREATE TABLE IF NOT EXISTS semantic_cache (
            id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
            tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
            model       text NOT NULL,
            prompt_hash bytea NOT NULL,
            provider    text NOT NULL DEFAULT '',
            embedding   vector(768) NOT NULL,
            response    jsonb NOT NULL,
            usage       jsonb NOT NULL DEFAULT '{}'::jsonb,
            cost_usd    numeric(14, 6) NOT NULL DEFAULT 0,
            expires_at  timestamptz NOT NULL,
            created_at  timestamptz NOT NULL DEFAULT now()
        )
    $ddl$;
    EXECUTE $ddl$
        CREATE INDEX IF NOT EXISTS semantic_cache_scope_idx
            ON semantic_cache(tenant_id, model, expires_at)
    $ddl$;
    EXECUTE $ddl$
        CREATE INDEX IF NOT EXISTS semantic_cache_embedding_idx
            ON semantic_cache USING hnsw (embedding vector_cosine_ops)
    $ddl$;
END $do$;`)
	return err
}
