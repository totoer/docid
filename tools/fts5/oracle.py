#!/usr/bin/env python3
"""Reproduce FTS5 fixtures with the pinned SQLite CLI (no Python SQLite dependency)."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
DATA = ROOT / "testdata" / "fts5"


def execute(sql, database=":memory:", binary="sqlite3", allow_error=False):
    result = subprocess.run([binary, "-batch", "-json", str(database)], input=sql,
                            text=True, capture_output=True, check=False)
    if result.returncode:
        if allow_error:
            return None
        raise RuntimeError(result.stderr.strip())
    return json.loads(result.stdout) if result.stdout.strip() else []


def metadata(binary="sqlite3"):
    version = execute("SELECT sqlite_version() AS version, sqlite_source_id() AS source_id;", binary=binary)[0]
    version["compile_options"] = sorted(row["compile_options"] for row in execute("PRAGMA compile_options;", binary=binary))
    if "ENABLE_FTS5" not in version["compile_options"]:
        raise RuntimeError("SQLite must enable FTS5")
    return version


def verify(binary="sqlite3", manifest=None):
    pinned = json.loads((manifest or DATA / "manifest.json").read_text())
    actual = metadata(binary)
    if actual != pinned:
        raise RuntimeError("SQLite version/source_id/compile_options differ from manifest; review and explicitly update the baseline")
    return actual


def identifier(value):
    return '"' + value.replace('"', '""') + '"'


def text_value(value):
    # The input is a SQL value encoded as bytes, never SQL syntax. This also
    # preserves embedded NUL and quotes in documents, options and MATCH input.
    if value is None:
        return "NULL"
    return "CAST(X'" + value.encode("utf-8").hex() + "' AS TEXT)"


def sql_string(value):
    return "'" + value.replace("'", "''") + "'"


def generate(binary="sqlite3", output=None):
    actual = verify(binary)
    corpus = json.loads((DATA / "corpus.json").read_text())
    expected = {"schema": 1, "oracle": actual, "cases": []}
    with tempfile.TemporaryDirectory(prefix="docid-fts5-") as directory:
        for number, case in enumerate(corpus["cases"]):
            database = Path(directory) / f"case-{number}.sqlite"
            fields = case["fields"]
            columns = ",".join(identifier(field) for field in fields)
            create = "CREATE VIRTUAL TABLE ft USING fts5(" + columns + ", tokenize=" + sql_string(case["tokenizer"] or "unicode61") + ");"
            if execute(create, database, binary, True) is None:
                expected["cases"].append({"name": case["name"], "invalid_definition": True})
                continue
            script = []
            for rowid, document in enumerate(case["documents"], 1):
                values = ",".join(text_value(value) for value in document["values"])
                script.append(f"INSERT INTO ft(rowid,{columns}) VALUES({rowid},{values});")
            execute("\n".join(script), database, binary)
            tokens = execute("CREATE VIRTUAL TABLE vocab USING fts5vocab(ft, 'instance'); SELECT hex(term) AS term_hex,doc,col,offset FROM vocab ORDER BY doc,col,offset,term;", database, binary)
            queries = []
            for query in case["queries"]:
                rows = execute("SELECT rowid FROM ft WHERE ft MATCH " + text_value(query) + " ORDER BY rowid;", database, binary, True)
                queries.append({"query": query, "error": rows is None,
                                "ids": [] if rows is None else sorted(case["documents"][row["rowid"] - 1]["id"] for row in rows)})
            expected["cases"].append({"name": case["name"], "invalid_definition": False, "tokens": tokens, "queries": queries})
    serialized = json.dumps(expected, ensure_ascii=True, indent=2) + "\n"
    destination = output or DATA / "expected.json"
    destination.write_text(serialized)
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["metadata", "verify", "generate"])
    parser.add_argument("--sqlite", default=os.environ.get("SQLITE3", "sqlite3"))
    parser.add_argument("--manifest", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.command == "metadata":
        print(json.dumps(metadata(args.sqlite), indent=2))
    elif args.command == "verify":
        verify(args.sqlite, args.manifest)
        print("SQLite FTS5 baseline matches")
    else:
        print(generate(args.sqlite, args.output))


if __name__ == "__main__":
    main()
