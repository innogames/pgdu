package pg

import (
	"context"
	"fmt"
)

// ReindexIndex runs REINDEX INDEX CONCURRENTLY on the named index. The index
// is resolved in the schema of the parent table — index relnames returned by
// sqlIndexes are bare, without schema.
func (c *Client) ReindexIndex(ctx context.Context, t Table, indexName string) error {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return err
	}
	stmt := "REINDEX INDEX CONCURRENTLY " + qualifiedIdent(t.Schema, indexName)
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("reindex %q.%q: %w", t.Schema, indexName, err)
	}
	return nil
}
