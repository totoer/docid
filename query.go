package docid

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Row maps aliases to documents. An unmatched LEFT JOIN alias has a nil value.
// Returned documents are detached from storage. Aliases in one result can share
// document maps; modifying a result never changes the database.
type Row map[string]Document

type join struct {
	collection, alias string
	on                Expr
	left              bool
}

// Sort specifies one sort field. Missing and non-scalar values precede null in
// ascending order.
type Sort struct {
	field Field
	desc  bool
}

func Asc(field Field) Sort  { return Sort{field: field} }
func Desc(field Field) Sort { return Sort{field: field, desc: true} }

// Query is an immutable reusable builder. Fields must include their source alias.
// A transaction-bound query can execute only inside its Update callback,
// sequentially with other operations on that transaction.
type Query struct {
	db                *DB
	tx                *Tx
	collection, alias string
	joins             []join
	where             []Expr
	order             []Sort
	limit, offset     int
	err               error
}

func (db *DB) From(collection string) Query {
	return Query{db: db, collection: collection, alias: collection, limit: -1}
}

type txQueryContext struct {
	tx    *Tx
	query context.Context
}

func (ctx txQueryContext) Err() error {
	if err := ctx.tx.check(); err != nil {
		return err
	}
	return ctx.query.Err()
}

func (q Query) operationContext(ctx context.Context) (operationContext, error) {
	if q.tx != nil {
		if err := q.tx.check(); err != nil {
			return nil, err
		}
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidQuery)
	}
	if q.tx != nil {
		return txQueryContext{tx: q.tx, query: ctx}, nil
	}
	return ctx, nil
}
func (q Query) As(alias string) Query { q.alias = alias; return q }
func (q Query) Where(expr Expr) Query {
	q.where = append(append([]Expr(nil), q.where...), expr)
	return q
}
func (q Query) InnerJoin(collection, alias string, on Expr) Query {
	q.joins = append(append([]join(nil), q.joins...), join{collection: collection, alias: alias, on: on})
	return q
}
func (q Query) LeftJoin(collection, alias string, on Expr) Query {
	q.joins = append(append([]join(nil), q.joins...), join{collection: collection, alias: alias, on: on, left: true})
	return q
}
func (q Query) OrderBy(order ...Sort) Query {
	q.order = append(append([]Sort(nil), q.order...), order...)
	return q
}
func (q Query) Limit(n int) Query {
	if n < 0 {
		q.err = fmt.Errorf("%w: negative limit", ErrInvalidQuery)
	}
	q.limit = n
	return q
}
func (q Query) Offset(n int) Query {
	if n < 0 {
		q.err = fmt.Errorf("%w: negative offset", ErrInvalidQuery)
	}
	q.offset = n
	return q
}

func validAlias(alias string) bool { return validName(alias) && !strings.Contains(alias, ".") }

func (q Query) validate(v readView) error {
	if q.err != nil {
		return q.err
	}
	if !validAlias(q.alias) {
		return fmt.Errorf("%w: invalid alias %q", ErrInvalidQuery, q.alias)
	}
	if _, ok := v.collections[q.collection]; !ok {
		return fmt.Errorf("%w: %s", ErrCollectionNotFound, q.collection)
	}
	aliases := map[string]string{q.alias: q.collection}
	for _, j := range q.joins {
		if !validAlias(j.alias) {
			return fmt.Errorf("%w: invalid alias %q", ErrInvalidQuery, j.alias)
		}
		if _, exists := aliases[j.alias]; exists {
			return fmt.Errorf("%w: duplicate alias %q", ErrInvalidQuery, j.alias)
		}
		if _, exists := v.collections[j.collection]; !exists {
			return fmt.Errorf("%w: %s", ErrCollectionNotFound, j.collection)
		}
		aliases[j.alias] = j.collection
		if err := j.on.validate(aliases); err != nil {
			return err
		}
	}
	for _, e := range q.where {
		if err := e.validate(aliases); err != nil {
			return err
		}
	}
	for _, s := range q.order {
		if err := validateField(s.field, aliases); err != nil {
			return err
		}
	}
	return nil
}

type record struct {
	id  string
	doc Document
	raw string
}

type recordMode struct {
	document bool
	raw      bool
}

// Execution rows use alias positions instead of maps. Record pointers preserve
// document sharing across JOIN matches; public maps are made only for the final
// result of All. Typed results reuse the stored JSON kept by their records.
type queryRow []*record

type queryResult struct {
	aliases []string
	rows    []queryRow
}

func (row queryRow) resolver(aliases map[string]int) fieldResolver {
	return func(field Field) (any, bool) {
		alias, path := splitField(field)
		position, ok := aliases[alias]
		if !ok || row[position] == nil {
			return nil, false
		}
		return lookup(row[position].doc, path)
	}
}

func (v readView) records(ctx operationContext, collection string, ids []string, restricted bool, mode recordMode) ([]record, error) {
	records := make([]record, 0)
	if restricted {
		records = make([]record, 0, len(ids))
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			item, err := v.getRecord(collection, id, mode)
			if err != nil {
				return nil, err
			}
			records = append(records, item)
		}
		return records, nil
	}
	err := v.scanRecords(collection, mode, func(item record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		records = append(records, item)
		return nil
	})
	sort.Slice(records, func(i, j int) bool { return records[i].id < records[j].id })
	return records, err
}

// probe recognizes an equijoin on an indexed field, including an equality
// within an AND. A non-nil index with false means the probe is missing or
// non-scalar and no candidates can match. The full ON expression is still checked.
func (j join) probe(v readView, resolve fieldResolver) (*index, scalar, bool) {
	var probe func(Expr) (*index, scalar, bool)
	probe = func(e Expr) (*index, scalar, bool) {
		if e.op == "and" {
			for _, child := range e.children {
				if idx, value, ok := probe(child); idx != nil {
					return idx, value, ok
				}
			}
		}
		if e.op != "eq" || e.right == "" {
			return nil, scalar{}, false
		}
		for _, pair := range [][2]Field{{e.field, e.right}, {e.right, e.field}} {
			alias, path := splitField(pair[0])
			otherAlias, _ := splitField(pair[1])
			if alias != j.alias || otherAlias == j.alias {
				continue
			}
			idx := v.fieldIndex(j.collection, path)
			if idx == nil {
				continue
			}
			value, exists := resolve(pair[1])
			if !exists {
				return idx, scalar{}, false
			}
			s, ok := scalarOf(value)
			if !ok {
				return idx, scalar{}, false
			}
			return idx, s, true
		}
		return nil, scalar{}, false
	}
	return probe(j.on)
}

// All executes a query. Results are deterministic: source document IDs, followed
// by joined IDs, provide the stable order unless OrderBy is specified.
func (q Query) All(ctx context.Context) ([]Row, error) {
	opCtx, err := q.operationContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := q.execute(opCtx, false)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, len(result.rows))
	for i, row := range result.rows {
		if err := opCtx.Err(); err != nil {
			return nil, err
		}
		out := make(Row, len(result.aliases))
		for slot, alias := range result.aliases {
			var doc Document
			if row[slot] != nil {
				doc = row[slot].doc
			}
			out[alias] = doc
		}
		rows[i] = out
	}
	return rows, nil
}

// execute owns the records it returns, including raw JSON when requested.
// Ordinary queries release the database lock before any user decoder runs.
func (q Query) execute(ctx operationContext, retainRaw bool) (*queryResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidQuery)
	}
	var v readView
	if q.tx != nil {
		if err := q.tx.check(); err != nil {
			return nil, err
		}
		v = q.tx.state.view
	} else {
		if q.db == nil {
			return nil, fmt.Errorf("%w: uninitialized query", ErrInvalidQuery)
		}
		q.db.mu.RLock()
		defer q.db.mu.RUnlock()
		if q.db.closed {
			return nil, ErrClosed
		}
		v = q.db.readView()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := q.validate(v); err != nil {
		return nil, err
	}
	bound, err := q.bindText(ctx, v)
	if err != nil {
		return nil, err
	}
	q = bound
	result := &queryResult{aliases: make([]string, 1, len(q.joins)+1)}
	result.aliases[0] = q.alias
	positions := map[string]int{q.alias: 0}
	for _, j := range q.joins {
		positions[j.alias] = len(result.aliases)
		result.aliases = append(result.aliases, j.alias)
	}
	if q.limit == 0 {
		return result, nil
	}
	// The builder is immutable, so the executor can read its predicates without
	// copying the child slice as the public And constructor does.
	predicate := Expr{op: "and", children: q.where}
	idx, op, value := predicate.indexedTerm(v, q.collection, q.alias)
	var ids []string
	restricted := idx != nil
	if idx != nil {
		ids = idx.matches(op, value)
	} else {
		ids, restricted = predicate.textCandidates(q.alias)
	}
	// A plain typed scan can decode stored JSON directly into the destination;
	// document maps are only needed when evaluating expressions or sort keys.
	mode := recordMode{
		raw:      retainRaw,
		document: !retainRaw || len(q.where) != 0 || len(q.joins) != 0 || len(q.order) != 0,
	}
	source, err := v.records(ctx, q.collection, ids, restricted, mode)
	if err != nil {
		return nil, err
	}
	rows := make([]queryRow, 0, len(source))
	// Store source-row slots together instead of allocating a slice per document.
	slots := make([]*record, 0, len(source)*len(result.aliases))
	scratch := make(queryRow, len(result.aliases))
	resolve := scratch.resolver(positions)
	resolveID := scratch.idResolver(positions)
	for i := range source {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scratch[0] = &source[i]
		if len(q.joins) != 0 || len(q.where) == 0 || predicate.evalResolved(resolve, resolveID) {
			start := len(slots)
			slots = append(slots, scratch...)
			rows = append(rows, queryRow(slots[start:len(slots):len(slots)]))
		}
	}
	for ji, j := range q.joins {
		slot := ji + 1
		next := make([]queryRow, 0)
		var all []record
		loaded := false
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			resolve := row.resolver(positions)
			resolveID := row.idResolver(positions)
			var candidates []record
			if idx, value, valid := j.probe(v, resolve); idx != nil {
				if valid {
					candidates, err = v.records(ctx, j.collection, idx.matches("eq", value), true, mode)
				}
			} else {
				if !loaded {
					all, err = v.records(ctx, j.collection, nil, false, mode)
					loaded = true
				}
				candidates = all
			}
			if err != nil {
				return nil, err
			}
			matched := false
			for ci := range candidates {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				// Reuse the input as a candidate; copy only matches. No candidate
				// map or slice is allocated for rejected pairs.
				row[slot] = &candidates[ci]
				if j.on.evalResolved(resolve, resolveID) {
					matched = true
					next = append(next, append(queryRow(nil), row...))
				}
			}
			if j.left && !matched {
				row[slot] = nil
				next = append(next, row)
			}
		}
		rows = next
	}
	if len(q.joins) != 0 {
		filtered := rows[:0]
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if predicate.evalResolved(row.resolver(positions), row.idResolver(positions)) {
				filtered = append(filtered, row)
			}
		}
		clear(rows[len(filtered):])
		rows = filtered
	}
	if len(q.order) > 0 {
		if err := sortRows(ctx, rows, q.order, positions); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := min(q.offset, len(rows))
	end := len(rows)
	if q.limit >= 0 && q.limit < end-start {
		end = start + q.limit
	}
	result.rows = rows[start:end]
	return result, nil
}

type sortKey struct {
	value  scalar
	exists bool
}

// Normalize sort values once per row, rather than allocating numeric scalars
// and walking field paths for every comparison in the sorting algorithm.
func sortRows(ctx operationContext, rows []queryRow, order []Sort, positions map[string]int) error {
	type keyedRow struct {
		row  queryRow
		keys []sortKey
	}
	keys := make([]sortKey, len(rows)*len(order))
	keyed := make([]keyedRow, len(rows))
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		keyed[i] = keyedRow{row: row, keys: keys[i*len(order) : (i+1)*len(order)]}
		resolve := row.resolver(positions)
		for j, s := range order {
			value, exists := resolve(s.field)
			if exists {
				keyed[i].keys[j].value, keyed[i].keys[j].exists = scalarOf(value)
			}
		}
	}
	sort.SliceStable(keyed, func(i, j int) bool {
		for k, s := range order {
			a, z := keyed[i].keys[k], keyed[j].keys[k]
			cmp := 0
			if a.exists != z.exists {
				if a.exists {
					cmp = 1
				} else {
					cmp = -1
				}
			} else if a.exists {
				cmp = compare(a.value, z.value)
			}
			if cmp != 0 {
				if s.desc {
					return cmp > 0
				}
				return cmp < 0
			}
		}
		return false
	})
	for i := range rows {
		rows[i] = keyed[i].row
	}
	return nil
}

// AllInto executes the query and decodes its rows into a pointer to a slice.
// Each element retains the Row shape: JSON keys are query aliases, so use
// fields such as User User `json:"u"`. Use pointer fields for nullable LEFT JOINs.
// The slice is replaced on success (empty results produce a non-nil empty slice).
// On error dst is unchanged. JSON decoding follows the same rules as GetInto.
// Transaction-bound queries run user decoders inside the Update callback;
// ordinary queries release the database lock before invoking them.
func (q Query) AllInto(ctx context.Context, dst any) error {
	if q.tx != nil {
		if err := q.tx.check(); err != nil {
			return err
		}
	}
	target, err := decodeDestination(dst, true)
	if err != nil {
		return err
	}
	opCtx, err := q.operationContext(ctx)
	if err != nil {
		return err
	}
	result, err := q.execute(opCtx, true)
	if err != nil {
		return err
	}
	raw, err := result.json(opCtx, -1)
	if err != nil {
		return err
	}
	return decodeInto(opCtx, raw, target)
}

// SelectInto executes the query and decodes one alias's documents into a pointer
// to a slice, e.g. []User or []*User. Filtering, joins, order and pagination apply
// as in All; joins may repeat a document. An unmatched LEFT JOIN decodes as nil
// for pointer elements, or a zero value for struct elements. The slice is replaced
// on success; on error dst is unchanged. Decoding follows GetInto's JSON rules.
// Transaction-bound queries run user decoders inside the Update callback;
// ordinary queries release the database lock before invoking them.
func (q Query) SelectInto(ctx context.Context, alias string, dst any) error {
	if q.tx != nil {
		if err := q.tx.check(); err != nil {
			return err
		}
	}
	target, err := decodeDestination(dst, true)
	if err != nil {
		return err
	}
	opCtx, err := q.operationContext(ctx)
	if err != nil {
		return err
	}
	result, err := q.execute(opCtx, true)
	if err != nil {
		return err
	}
	position := -1
	for i, known := range result.aliases {
		if known == alias {
			position = i
			break
		}
	}
	if position < 0 {
		return fmt.Errorf("%w: unknown alias %q", ErrInvalidQuery, alias)
	}
	raw, err := result.json(opCtx, position)
	if err != nil {
		return err
	}
	return decodeInto(opCtx, raw, target)
}
