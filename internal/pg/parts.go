package pg

import "context"

// TableParts returns the heap, the toast relation (if non-zero), and every
// index of a table as a slice of Parts. Sizes come from the parent Table row
// (we don't re-issue pg_relation_size for heap/toast). Bloat is filled in
// separately by FillBloat.
func (c *Client) TableParts(ctx context.Context, t Table) ([]Part, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	parts := []Part{
		{Kind: PartHeap, Name: "heap", SizeBytes: t.HeapBytes},
	}
	if t.ToastBytes > 0 {
		parts = append(parts, Part{Kind: PartToast, Name: "toast", SizeBytes: t.ToastBytes})
	}
	rows, err := pool.Query(ctx, sqlIndexes, t.OID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var oid uint32
		var p Part
		p.Kind = PartIndex
		if err := rows.Scan(&oid, &p.Name, &p.SizeBytes, &p.IsPrimary, &p.IsUnique, &p.AccessMethod); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}
