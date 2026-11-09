package docid

import (
	"context"
	"errors"
	"maps"
	"sort"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

// Tx is a read-write transaction valid only inside its Update callback.
// Use it sequentially; transaction handles are not safe for concurrent use.
// Copies of a Tx share the same lifetime and state.
type Tx struct {
	state *txState
}

type txState struct {
	ctx      context.Context
	batch    *pebble.Batch
	view     readView
	active   bool
	writable map[string]bool
}

// TxCollection is a collection bound to the lifetime of an Update callback.
// Use it sequentially, through the transaction rather than ordinary DB handles.
type TxCollection struct {
	tx   *Tx
	name string
}

// Update runs fn once with exclusive access to the database. A nil callback
// result commits all changes durably; an error or panic discards them. Panics
// propagate unchanged. The transaction and its handles expire when fn returns.
//
// Use only tx to access this database from fn, including from JSON hooks.
// Ordinary DB/Collection methods, Close and nested Update calls wait for this
// transaction and would deadlock if fn waits for them. Do not share tx between
// goroutines or wait for another operation on the same DB from fn.
//
// Context cancellation is checked before fn, during operations and before
// commit. Waiting for the lock and an already-started commit are not canceled.
// A running fn must return before cancellation can release the database lock.
func (db *DB) Update(ctx context.Context, fn func(*Tx) error) error {
	return db.update(ctx, fn, func(batch *pebble.Batch) error {
		return batch.Commit(pebble.Sync)
	})
}

// update keeps the commit boundary explicit: cancellation after this call starts
// cannot replace a successful commit result. The caller owns commit policy.
func (db *DB) update(ctx context.Context, fn func(*Tx) error, commit func(*pebble.Batch) error) error {
	if ctx == nil || fn == nil {
		return ErrInvalidTransaction
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	batch := db.store.NewIndexedBatch()
	state := &txState{
		ctx: ctx, batch: batch, active: true, writable: make(map[string]bool),
		view: readView{reader: batch, collections: maps.Clone(db.collections)},
	}
	tx := &Tx{state: state}
	defer func() {
		state.active = false
		_ = batch.Close()
		state.batch = nil
		state.view = readView{}
		state.ctx = nil
		state.writable = nil
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !batch.Empty() {
		if err := commit(batch); err != nil {
			return err
		}
		db.collections = state.view.collections
	}
	return nil
}

func (tx *Tx) check() error {
	if tx == nil || tx.state == nil || !tx.state.active {
		return ErrTxClosed
	}
	return tx.state.ctx.Err()
}

// Collection returns a collection, creating it in this transaction if needed.
// Its creation is committed or rolled back together with document changes.
func (tx *Tx) Collection(name string) (*TxCollection, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if !validName(name) {
		return nil, ErrInvalidName
	}
	if _, ok := tx.state.view.collections[name]; !ok {
		if err := tx.state.batch.Set([]byte("c/"+enc(name)), []byte("{}"), nil); err != nil {
			return nil, err
		}
		tx.state.view.collections[name] = newCollectionState()
	}
	return &TxCollection{tx: tx, name: name}, nil
}

// Collections returns transaction-visible collection names in lexical order.
func (tx *Tx) Collections() ([]string, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tx.state.view.collections))
	for name := range tx.state.view.collections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Name returns the collection name, including after its transaction ends.
func (c *TxCollection) Name() string { return c.name }

// From starts a query against this transaction's documents and indexes.
// The query does not create missing collections and expires with the callback.
func (tx *Tx) From(collection string) Query {
	if tx == nil {
		tx = &Tx{}
	}
	return Query{tx: tx, collection: collection, alias: collection, limit: -1}
}

// Find starts a query against this transaction's collection.
func (c *TxCollection) Find() Query { return c.tx.From(c.name) }

// Get returns the latest transaction-visible document as a detached map.
func (c *TxCollection) Get(id string) (Document, error) {
	if err := c.tx.check(); err != nil {
		return nil, err
	}
	return c.tx.state.view.get(c.name, id)
}

// GetInto decodes the latest transaction-visible JSON into dst, following
// Collection.GetInto's JSON rules. On error dst is unchanged. User JSON hooks
// run inside the transaction: they may access tx, but not ordinary DB handles.
func (c *TxCollection) GetInto(id string, dst any) error {
	if err := c.tx.check(); err != nil {
		return err
	}
	target, err := decodeDestination(dst, false)
	if err != nil {
		return err
	}
	item, err := c.tx.state.view.getRecord(c.name, id, recordMode{raw: true})
	if err != nil {
		return err
	}
	return decodeInto(c.tx.state.ctx, []byte(item.raw), target)
}

// Insert creates a document in this transaction, rejecting an existing ID.
func (c *TxCollection) Insert(id string, value any) error { return c.write(id, value, false) }

// Put creates or replaces a whole document in this transaction.
// Validation/constraint errors leave this operation's state unchanged; the
// callback can handle the error and continue, or return it for full rollback.
func (c *TxCollection) Put(id string, value any) error { return c.write(id, value, true) }

func (c *TxCollection) write(id string, value any, replace bool) error {
	if err := c.tx.check(); err != nil {
		return err
	}
	if id == "" || !utf8.ValidString(id) {
		return ErrInvalidName
	}
	raw, err := marshalDocument(value)
	if err != nil {
		return err
	}
	// MarshalJSON can access tx or cancel its context. Read state afterwards.
	if err := c.tx.check(); err != nil {
		return err
	}
	state := c.tx.state
	current := state.view.collections[c.name]
	indexes := current.indexes
	old, err := state.view.readDocument(c.name, id, current.hasIndexes())
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && !replace {
		return ErrAlreadyExists
	}
	var doc Document
	if current.hasIndexes() {
		doc, err = decodeDocument(raw)
		if err != nil {
			return err
		}
	}
	for _, idx := range indexes {
		if err := idx.check(id, doc); err != nil {
			return err
		}
	}
	changes, err := current.prepareText(state.ctx, id, old, doc)
	if err != nil {
		return err
	}
	if err := c.tx.check(); err != nil {
		return err
	}
	writable := state.writableCollection(c.name)
	indexes = writable.indexes
	if err := state.batch.Set(docKey(c.name, id), raw, nil); err != nil {
		return err
	}
	for _, idx := range indexes {
		idx.remove(id, old)
		idx.add(id, doc)
	}
	writable.applyText(changes)
	return nil
}

// writableCollection detaches a collection's indexes once, before any mutation.
// Both the wrapper map and B-trees must be private, even for rollback on panic.
func (state *txState) writableCollection(collection string) *collectionState {
	if !state.writable[collection] {
		indexes := state.view.collections[collection]
		copies := newCollectionState()
		for name, idx := range indexes.indexes {
			copies.indexes[name] = &index{def: idx.def, tree: idx.tree.Clone()}
		}
		for name, idx := range indexes.textIndexes {
			copies.textIndexes[name] = idx.clone()
		}
		state.view.collections[collection] = copies
		state.writable[collection] = true
	}
	return state.view.collections[collection]
}

// Delete removes the latest transaction-visible document.
// An unknown ID returns ErrNotFound without changing the transaction.
func (c *TxCollection) Delete(id string) error {
	if err := c.tx.check(); err != nil {
		return err
	}
	state := c.tx.state
	old, err := state.view.readDocument(c.name, id, state.view.collections[c.name].hasIndexes())
	if err != nil {
		return err
	}
	changes, err := state.view.collections[c.name].prepareText(state.ctx, id, old, nil)
	if err != nil {
		return err
	}
	if err := c.tx.check(); err != nil {
		return err
	}
	writable := state.writableCollection(c.name)
	indexes := writable.indexes
	if err := state.batch.Delete(docKey(c.name, id), nil); err != nil {
		return err
	}
	for _, idx := range indexes {
		idx.remove(id, old)
	}
	writable.applyText(changes)
	return nil
}
