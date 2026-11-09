package docid

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const textIndexFormat = 1
const textIndexProfile = "fts5-unicode61-v1"

type storedTextIndex struct {
	Version    int       `json:"version"`
	Profile    string    `json:"profile"`
	Definition TextIndex `json:"definition"`
}

func textIndexKey(collection, name string) []byte {
	return []byte("t/" + enc(collection) + "/" + enc(name))
}

// CreateTextIndex builds a positional index and durably saves its definition.
// Index definitions cannot be changed inside Update; create them beforehand.
func (c *Collection) CreateTextIndex(def TextIndex) error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.db.closed {
		return ErrClosed
	}
	idx, err := newTextIndex(def)
	if err != nil {
		return err
	}
	indexes := c.db.collections[c.name].textIndexes
	if _, exists := indexes[def.Name]; exists {
		return ErrAlreadyExists
	}
	if err := c.db.readView().scan(c.name, idx.add); err != nil {
		return err
	}
	raw, err := json.Marshal(storedTextIndex{Version: textIndexFormat, Profile: textIndexProfile, Definition: idx.def})
	if err != nil {
		return err
	}
	if err := c.db.store.Set(textIndexKey(c.name, def.Name), raw, pebble.Sync); err != nil {
		return err
	}
	indexes[def.Name] = idx
	return nil
}

// TextIndexes returns detached definitions sorted by name. It does not include
// scalar indexes; the two index kinds have independent name spaces.
func (c *Collection) TextIndexes() ([]TextIndex, error) {
	c.db.mu.RLock()
	defer c.db.mu.RUnlock()
	if c.db.closed {
		return nil, ErrClosed
	}
	indexes := c.db.collections[c.name].textIndexes
	defs := make([]TextIndex, 0, len(indexes))
	for _, idx := range indexes {
		def := idx.def
		def.Fields = append([]string(nil), def.Fields...)
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

// DropTextIndex durably removes a text index. Subsequent Match calls that refer
// to it fail with ErrInvalidQuery until the index is created again.
func (c *Collection) DropTextIndex(name string) error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.db.closed {
		return ErrClosed
	}
	indexes := c.db.collections[c.name].textIndexes
	if _, exists := indexes[name]; !exists {
		return ErrNotFound
	}
	if err := c.db.store.Delete(textIndexKey(c.name, name), pebble.Sync); err != nil {
		return err
	}
	delete(indexes, name)
	return nil
}

func (db *DB) loadTextIndexes() error {
	return db.readView().walk("t/", func(key string, raw []byte) error {
		parts := strings.Split(key, "/")
		if len(parts) != 3 {
			return fmt.Errorf("docid: corrupt text index key %q", key)
		}
		name, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return err
		}
		state, ok := db.collections[string(name)]
		if !ok {
			return ErrCollectionNotFound
		}
		var stored storedTextIndex
		if err := json.Unmarshal(raw, &stored); err != nil {
			return err
		}
		if stored.Version != textIndexFormat || stored.Profile != textIndexProfile || enc(stored.Definition.Name) != parts[2] {
			return fmt.Errorf("docid: invalid text index format/profile or key")
		}
		idx, err := newTextIndex(stored.Definition)
		if err != nil {
			return fmt.Errorf("docid: corrupt text index definition: %w", err)
		}
		state.textIndexes[idx.def.Name] = idx
		return nil
	})
}

type textMutation struct{ old, next []textEntry }

func (s *collectionState) prepareText(ctx operationContext, id string, old, next Document) (map[string]textMutation, error) {
	if len(s.textIndexes) == 0 {
		return nil, nil
	}
	changes := make(map[string]textMutation, len(s.textIndexes))
	for name, idx := range s.textIndexes {
		before, err := idx.entries(ctx, id, old)
		if err != nil {
			return nil, err
		}
		after, err := idx.entries(ctx, id, next)
		if err != nil {
			return nil, err
		}
		changes[name] = textMutation{before, after}
	}
	return changes, nil
}
func (s *collectionState) applyText(changes map[string]textMutation) {
	for name, change := range changes {
		s.textIndexes[name].apply(change.old, change.next)
	}
}
