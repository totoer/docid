package docid

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

var (
	ErrClosed             = errors.New("docid: database closed")
	ErrNotFound           = errors.New("docid: document not found")
	ErrCollectionNotFound = errors.New("docid: collection not found")
	ErrAlreadyExists      = errors.New("docid: already exists")
	ErrUniqueConstraint   = errors.New("docid: unique index violation")
	ErrInvalidDocument    = errors.New("docid: expected a JSON object")
	ErrInvalidQuery       = errors.New("docid: invalid query")
	ErrInvalidName        = errors.New("docid: invalid name")
	ErrInvalidDestination = errors.New("docid: invalid decode destination")
	ErrInvalidTransaction = errors.New("docid: invalid transaction")
	ErrTxClosed           = errors.New("docid: transaction closed")
)

// DB is safe for concurrent use. A query observes one consistent database state.
type DB struct {
	mu          sync.RWMutex
	store       *pebble.DB
	collections map[string]*collectionState
	closed      bool
}

// Open opens or creates a database directory and rebuilds its persisted indexes.
func Open(path string) (*DB, error) {
	store, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, err
	}
	db := &DB{store: store, collections: make(map[string]*collectionState)}
	if err := db.load(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return db, nil
}

// Close waits for in-flight operations. Repeated Close calls succeed.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true
	return db.store.Close()
}

func validName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.ValidString(name)
}
func enc(value string) string             { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
func docPrefix(collection string) string  { return "d/" + enc(collection) + "/" }
func docKey(collection, id string) []byte { return []byte(docPrefix(collection) + enc(id)) }
func indexKey(collection, name string) []byte {
	return []byte("i/" + enc(collection) + "/" + enc(name))
}

// Collection creates a named collection if needed and returns its handle.
func (db *DB) Collection(name string) (*Collection, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil, ErrClosed
	}
	if !validName(name) {
		return nil, ErrInvalidName
	}
	if _, ok := db.collections[name]; !ok {
		if err := db.store.Set([]byte("c/"+enc(name)), []byte("{}"), pebble.Sync); err != nil {
			return nil, err
		}
		db.collections[name] = newCollectionState()
	}
	return &Collection{db: db, name: name}, nil
}

// Collections returns collection names in lexical order.
func (db *DB) Collections() ([]string, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	names := make([]string, 0, len(db.collections))
	for name := range db.collections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (db *DB) load() error {
	if err := db.readView().walk("c/", func(key string, _ []byte) error {
		name, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "c/"))
		if err != nil || !validName(string(name)) {
			return fmt.Errorf("docid: corrupt collection key %q", key)
		}
		db.collections[string(name)] = newCollectionState()
		return nil
	}); err != nil {
		return err
	}
	if err := db.readView().walk("i/", func(key string, raw []byte) error {
		parts := strings.Split(key, "/")
		if len(parts) != 3 {
			return fmt.Errorf("docid: corrupt index key %q", key)
		}
		name, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return err
		}
		indexes, ok := db.collections[string(name)]
		if !ok {
			return ErrCollectionNotFound
		}
		var def Index
		if err := json.Unmarshal(raw, &def); err != nil {
			return err
		}
		if !validName(def.Name) || !validPath(def.Field) || enc(def.Name) != parts[2] {
			return fmt.Errorf("docid: corrupt index definition")
		}
		indexes.indexes[def.Name] = newIndex(def)
		return nil
	}); err != nil {
		return err
	}
	if err := db.loadTextIndexes(); err != nil {
		return err
	}
	for name, indexes := range db.collections {
		if !indexes.hasIndexes() {
			continue
		}
		if err := db.readView().scan(name, func(id string, doc Document) error {
			for _, idx := range indexes.indexes {
				if err := idx.check(id, doc); err != nil {
					return err
				}
				idx.add(id, doc)
			}
			for _, idx := range indexes.textIndexes {
				if err := idx.add(id, doc); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
