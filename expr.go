package docid

import (
	"fmt"
	"strings"
)

// Field is an alias-qualified document path, for example F("u.address.city").
type Field string

func F(path string) Field { return Field(path) }

// Expr is an immutable predicate. Its zero value is invalid.
type Expr struct {
	op       string
	field    Field
	right    Field
	value    scalar
	children []Expr
	err      error
}

func (f Field) comparison(op string, value any) Expr {
	s, err := literal(value)
	return Expr{op: op, field: f, value: s, err: err}
}

func (f Field) Eq(value any) Expr  { return f.comparison("eq", value) }
func (f Field) Ne(value any) Expr  { return f.comparison("ne", value) }
func (f Field) Gt(value any) Expr  { return f.comparison("gt", value) }
func (f Field) Gte(value any) Expr { return f.comparison("gte", value) }
func (f Field) Lt(value any) Expr  { return f.comparison("lt", value) }
func (f Field) Lte(value any) Expr { return f.comparison("lte", value) }
func (f Field) EqField(other Field) Expr {
	if other == "" {
		return Expr{err: fmt.Errorf("%w: empty comparison field", ErrInvalidQuery)}
	}
	return Expr{op: "eq", field: f, right: other}
}
func (f Field) Exists() Expr { return Expr{op: "exists", field: f} }
func (f Field) IsNull() Expr { return f.Eq(nil) }
func (f Field) In(values ...any) Expr {
	children := make([]Expr, len(values))
	for i, v := range values {
		children[i] = f.Eq(v)
	}
	return Or(children...)
}

func And(exprs ...Expr) Expr { return Expr{op: "and", children: append([]Expr(nil), exprs...)} }
func Or(exprs ...Expr) Expr  { return Expr{op: "or", children: append([]Expr(nil), exprs...)} }
func Not(expr Expr) Expr     { return Expr{op: "not", children: []Expr{expr}} }

func splitField(f Field) (alias, path string) { alias, path, _ = strings.Cut(string(f), "."); return }

func validateField(f Field, aliases map[string]string) error {
	alias, path := splitField(f)
	if _, ok := aliases[alias]; !ok || !validPath(path) {
		return fmt.Errorf("%w: unknown alias or invalid field %q", ErrInvalidQuery, f)
	}
	return nil
}

func (e Expr) validate(aliases map[string]string) error {
	if e.err != nil {
		return e.err
	}
	switch e.op {
	case "and", "or", "not":
		for _, child := range e.children {
			if err := child.validate(aliases); err != nil {
				return err
			}
		}
		return nil
	case "eq", "ne", "lt", "lte", "gt", "gte", "exists":
		if err := validateField(e.field, aliases); err != nil {
			return err
		}
		if e.right != "" {
			return validateField(e.right, aliases)
		}
		return nil
	default:
		return fmt.Errorf("%w: empty expression", ErrInvalidQuery)
	}
}

func fieldValue(row Row, field Field) (any, bool) {
	alias, path := splitField(field)
	return lookup(row[alias], path)
}

func testComparison(op string, cmp int) bool {
	switch op {
	case "eq":
		return cmp == 0
	case "ne":
		return cmp != 0
	case "lt":
		return cmp < 0
	case "lte":
		return cmp <= 0
	case "gt":
		return cmp > 0
	case "gte":
		return cmp >= 0
	}
	return false
}

func (e Expr) eval(row Row) bool {
	return e.evalWith(func(field Field) (any, bool) { return fieldValue(row, field) })
}

// A resolver lets public rows and compact execution rows share the exact same
// expression semantics without constructing a map for every JOIN candidate.
type fieldResolver func(Field) (any, bool)

func (e Expr) evalWith(resolve fieldResolver) bool {
	switch e.op {
	case "and":
		for _, child := range e.children {
			if !child.evalWith(resolve) {
				return false
			}
		}
		return true
	case "or":
		for _, child := range e.children {
			if child.evalWith(resolve) {
				return true
			}
		}
		return false
	case "not":
		return !e.children[0].evalWith(resolve)
	}
	value, exists := resolve(e.field)
	if e.op == "exists" {
		return exists
	}
	if !exists {
		return false
	}
	a, ok := scalarOf(value)
	if !ok {
		return false
	}
	b := e.value
	if e.right != "" {
		value, exists = resolve(e.right)
		if !exists {
			return false
		}
		b, ok = scalarOf(value)
		if !ok {
			return false
		}
	}
	if a.kind != b.kind {
		return e.op == "ne"
	}
	return testComparison(e.op, compare(a, b))
}

// indexedTerm finds a safe restriction from a conjunction. OR/NOT never narrow
// the candidate set, and the entire predicate is evaluated after lookup.
func (e Expr) indexedTerm(db *DB, collection, alias string) (*index, string, scalar) {
	if e.op == "and" {
		for _, child := range e.children {
			if idx, op, value := child.indexedTerm(db, collection, alias); idx != nil {
				return idx, op, value
			}
		}
	}
	if e.right == "" && (e.op == "eq" || e.op == "gt" || e.op == "gte" || e.op == "lt" || e.op == "lte") {
		a, path := splitField(e.field)
		if a == alias {
			return db.fieldIndex(collection, path), e.op, e.value
		}
	}
	return nil, "", scalar{}
}
