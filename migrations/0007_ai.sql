CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS issue_embeddings (
    issue_id   TEXT PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
    embedding  vector(1536),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- IVF flat index on the embedding column. 100 lists is fine until we
-- cross ~100k issues — at that point bumping lists or moving to
-- HNSW becomes the next index decision.
CREATE INDEX IF NOT EXISTS idx_issue_embeddings_vector
    ON issue_embeddings
    USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
