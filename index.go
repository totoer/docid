package docid

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble"
	"github.com/google/btree"
)

// Index describes a sparse scalar-field index. Missing fields, objects and arrays
// are omitted. Explicit JSON null is indexed and participates in uniqueness.
type Index struct {
	Name   string `json:"name"`
	Field  string `json:"field"`
	Unique bool   `json:"unique,omitempty"`
}

type indexEntry struct {
	value scalar
	id    string
}
type index struct {
	def  Index
	tree *btree.BTreeG[indexEntry]
}

func newIndex(def Index) *index {
	return &index{def: def, tree: btree.NewG(32, func(a, b indexEntry) bool {
		if cmp := compare(a.value, b.value); cmp != 0 {
			return cmp < 0
		}
		return a.id < b.id
	})}
}

func (idx *index) entry(id string, doc Document) (indexEntry, bool) {
	value, ok := lookup(doc, idx.def.Field)
	if !ok {
		return indexEntry{}, false
	}
	s, ok := scalarOf(value)
	return indexEntry{s, id}, ok
}

func (idx *index) add(id string, doc Document) {
	if item, ok := idx.entry(id, doc); ok {
		idx.tree.ReplaceOrInsert(item)
	}
}

func (idx *index) remove(id string, doc Document) {
	if item, ok := idx.entry(id, doc); ok {
		idx.tree.Delete(item)
	}
}

func (idx *index) check(id string, doc Document) error {
	if !idx.def.Unique {
		return nil
	}
	entry, ok := idx.entry(id, doc)
	if !ok {
		return nil
	}
	conflict := false
	idx.tree.AscendGreaterOrEqual(indexEntry{value: entry.value}, func(item indexEntry) bool {
		if compare(item.value, entry.value) != 0 {
			return false
		}
		if item.id != id {
			conflict = true
			return false
		}
		return true
	})
	if conflict {
		return fmt.Errorf("%w: %s", ErrUniqueConstraint, idx.def.Name)
	}
	return nil
}

// matches returns candidate IDs using B-tree bounds; callers still evaluate the
// complete predicate, so using one term of an AND is always safe.
func (idx *index) matches(op string, value scalar) []string {
	ids := make([]string, 0)
	visit := func(item indexEntry) bool {
		if item.value.kind > value.kind {
			return false
		}
		cmp := compare(item.value, value)
		if op == "eq" && cmp > 0 {
			return false
		}
		if op == "lt" && cmp >= 0 || op == "lte" && cmp > 0 {
			return false
		}
		if item.value.kind == value.kind && testComparison(op, cmp) {
			ids = append(ids, item.id)
		}
		return true
	}
	switch op {
	case "eq", "gt", "gte":
		idx.tree.AscendGreaterOrEqual(indexEntry{value: value}, visit)
	default:
		idx.tree.Ascend(visit)
	}
	sort.Strings(ids)
	return ids
}

// CreateIndex builds an index on existing documents and persists its definition.
// A failed unique-index build leaves the database unchanged.
func (c *Collection) CreateIndex(def Index) error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.db.closed {
		return ErrClosed
	}
	if !validName(def.Name) || !validPath(def.Field) {
		return ErrInvalidName
	}
	indexes := c.db.collections[c.name]
	if _, exists := indexes[def.Name]; exists {
		return ErrAlreadyExists
	}
	idx := newIndex(def)
	if err := c.db.readView().scan(c.name, func(id string, doc Document) error {
		if err := idx.check(id, doc); err != nil {
			return err
		}
		idx.add(id, doc)
		return nil
	}); err != nil {
		return err
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return err
	}
	if err := c.db.store.Set(indexKey(c.name, def.Name), raw, pebble.Sync); err != nil {
		return err
	}
	indexes[def.Name] = idx
	return nil
}

// DropIndex removes the index and its persistent definition.
func (c *Collection) DropIndex(name string) error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.db.closed {
		return ErrClosed
	}
	if _, ok := c.db.collections[c.name][name]; !ok {
		return ErrNotFound
	}
	if err := c.db.store.Delete(indexKey(c.name, name), pebble.Sync); err != nil {
		return err
	}
	delete(c.db.collections[c.name], name)
	return nil
}

// Indexes returns definitions in lexical order by index name.
func (c *Collection) Indexes() ([]Index, error) {
	c.db.mu.RLock()
	defer c.db.mu.RUnlock()
	if c.db.closed {
		return nil, ErrClosed
	}
	defs := make([]Index, 0, len(c.db.collections[c.name]))
	for _, idx := range c.db.collections[c.name] {
		defs = append(defs, idx.def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func (v readView) fieldIndex(collection, field string) *index {
	// Choose deterministically if multiple indexes cover a field.
	var selected *index
	for _, idx := range v.collections[collection] {
		if idx.def.Field == field && (selected == nil || idx.def.Name < selected.def.Name) {
			selected = idx
		}
	}
	return selected
}
