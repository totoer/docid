package docid

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

// Collection is a handle to a collection of JSON objects with external IDs.
type Collection struct {
	db   *DB
	name string
}

// Name returns the collection name.
func (c *Collection) Name() string { return c.name }

// Insert creates a document. The ID is a separate key and is not added to JSON.
// value may be a map, struct or json.RawMessage; it must encode a JSON object.
func (c *Collection) Insert(id string, value any) error { return c.write(id, value, false) }

// Put inserts a document or replaces the entire existing document.
func (c *Collection) Put(id string, value any) error { return c.write(id, value, true) }

func (c *Collection) write(id string, value any, replace bool) error {
	if id == "" || !utf8.ValidString(id) {
		return ErrInvalidName
	}
	raw, err := marshalDocument(value)
	if err != nil {
		return err
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.db.closed {
		return ErrClosed
	}
	indexes := c.db.collections[c.name]
	old, err := c.db.readDocument(c.name, id, len(indexes) != 0)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && !replace {
		return ErrAlreadyExists
	}
	var doc Document
	if len(indexes) != 0 {
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
	if err := c.db.store.Set(docKey(c.name, id), raw, pebble.Sync); err != nil {
		return err
	}
	for _, idx := range indexes {
		idx.remove(id, old)
		idx.add(id, doc)
	}
	return nil
}

// Get returns a detached document that the caller may modify freely.
func (c *Collection) Get(id string) (Document, error) {
	c.db.mu.RLock()
	defer c.db.mu.RUnlock()
	if c.db.closed {
		return nil, ErrClosed
	}
	return c.db.get(c.name, id)
}

// GetInto reads a document into a non-nil pointer, typically *MyStruct.
// JSON tags and json.Unmarshaler are supported; interface numbers use json.Number.
// On success dst is replaced, including zeroing fields absent from the document.
// On error dst is unchanged. The external document ID is not injected into dst.
func (c *Collection) GetInto(id string, dst any) error {
	target, err := decodeDestination(dst, false)
	if err != nil {
		return err
	}
	raw, err := func() ([]byte, error) {
		c.db.mu.RLock()
		defer c.db.mu.RUnlock()
		if c.db.closed {
			return nil, ErrClosed
		}
		raw, closer, err := c.db.store.Get(docKey(c.name, id))
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		defer closer.Close()
		return bytes.Clone(raw), nil
	}()
	if err != nil {
		return err
	}
	return decodeInto(context.Background(), raw, target)
}

// Delete removes a document, returning ErrNotFound for an unknown ID.
func (c *Collection) Delete(id string) error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.db.closed {
		return ErrClosed
	}
	indexes := c.db.collections[c.name]
	old, err := c.db.readDocument(c.name, id, len(indexes) != 0)
	if err != nil {
		return err
	}
	if err := c.db.store.Delete(docKey(c.name, id), pebble.Sync); err != nil {
		return err
	}
	for _, idx := range indexes {
		idx.remove(id, old)
	}
	return nil
}

// Find starts a query using the collection name as its default alias.
func (c *Collection) Find() Query { return c.db.From(c.name) }

func (c *Collection) String() string { return fmt.Sprintf("Collection(%q)", c.name) }
