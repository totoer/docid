package docid

type textMatch struct {
	alias, index string
	ast          *textNode
}

// Match filters documents of alias through the named text index using SQLite
// FTS5 MATCH syntax and the index's unicode61 tokenizer. It does not rank rows.
// The index must exist at execution time; invalid queries wrap ErrInvalidQuery.
func Match(alias, indexName, query string) Expr {
	ast, err := parseTextQuery(query)
	return Expr{op: "match", text: &textMatch{alias: alias, index: indexName, ast: ast}, err: err}
}

func (e Expr) hasTextMatch() bool {
	if e.op == "match" {
		return true
	}
	for _, child := range e.children {
		if child.hasTextMatch() {
			return true
		}
	}
	return false
}
func (e Expr) bindText(ctx operationContext, v readView, aliases map[string]string) (Expr, error) {
	if !e.hasTextMatch() {
		return e, nil
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	if e.op == "match" {
		collection := v.collections[aliases[e.text.alias]]
		idx := collection.textIndexes[e.text.index]
		if idx == nil {
			return e, textSyntax("text index not found: " + e.text.index)
		}
		ids, err := idx.search(ctx, e.text.ast)
		if err != nil {
			return e, err
		}
		e.textMatches = ids
		return e, nil
	}
	e.children = append([]Expr(nil), e.children...)
	for i, child := range e.children {
		bound, err := child.bindText(ctx, v, aliases)
		if err != nil {
			return e, err
		}
		e.children[i] = bound
	}
	return e, nil
}
func (q Query) bindText(ctx operationContext, v readView) (Query, error) {
	aliases := map[string]string{q.alias: q.collection}
	for _, j := range q.joins {
		aliases[j.alias] = j.collection
	}
	whereCopied, joinsCopied := false, false
	for i, e := range q.where {
		if e.hasTextMatch() {
			if !whereCopied {
				q.where = append([]Expr(nil), q.where...)
				whereCopied = true
			}
			bound, err := e.bindText(ctx, v, aliases)
			if err != nil {
				return q, err
			}
			q.where[i] = bound
		}
	}
	for i, j := range q.joins {
		if j.on.hasTextMatch() {
			if !joinsCopied {
				q.joins = append([]join(nil), q.joins...)
				joinsCopied = true
			}
			bound, err := j.on.bindText(ctx, v, aliases)
			if err != nil {
				return q, err
			}
			q.joins[i].on = bound
		}
	}
	return q, nil
}

func (e Expr) textCandidates(alias string) ([]string, bool) {
	if e.op == "match" && e.text.alias == alias {
		return sortedTextCandidates(e.textMatches), true
	}
	if e.op == "and" {
		for _, child := range e.children {
			if ids, ok := child.textCandidates(alias); ok {
				return ids, true
			}
		}
	}
	return nil, false
}

func (row queryRow) idResolver(aliases map[string]int) documentIDResolver {
	return func(alias string) (string, bool) {
		slot, ok := aliases[alias]
		if !ok || row[slot] == nil {
			return "", false
		}
		return row[slot].id, true
	}
}
