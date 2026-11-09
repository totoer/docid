// Package docid provides an embedded, collection-oriented JSON document database.
// Pebble stores documents and index definitions durably. B-trees index scalar
// fields and positional text in memory and are rebuilt on Open. Queries use a
// Go DSL with joins and SQLite FTS5-compatible MATCH filters using unicode61.
// DB.Update provides exclusive, atomic read-write transactions across collections.
package docid
