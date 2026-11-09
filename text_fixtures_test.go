package docid

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type textFixtureDocument struct {
	ID     string    `json:"id"`
	Values []*string `json:"values"`
}

type textFixtureCase struct {
	Name      string                `json:"name"`
	Fields    []string              `json:"fields"`
	Tokenizer string                `json:"tokenizer"`
	Documents []textFixtureDocument `json:"documents"`
	Queries   []string              `json:"queries"`
}

type textFixtureResult struct {
	Name              string `json:"name"`
	InvalidDefinition bool   `json:"invalid_definition"`
	Tokens            []struct {
		Hex      string `json:"term_hex"`
		Document int    `json:"doc"`
		Column   string `json:"col"`
		Offset   int    `json:"offset"`
	} `json:"tokens"`
	Queries []struct {
		Query string   `json:"query"`
		Error bool     `json:"error"`
		IDs   []string `json:"ids"`
	} `json:"queries"`
}

func readTextFixture(t testing.TB, name string, dst any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/fts5/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatal(err)
	}
}

func loadTextFixtures(t testing.TB) ([]textFixtureCase, []textFixtureResult) {
	t.Helper()
	var corpus struct {
		Schema int               `json:"schema"`
		Cases  []textFixtureCase `json:"cases"`
	}
	var expected struct {
		Schema int                 `json:"schema"`
		Oracle json.RawMessage     `json:"oracle"`
		Cases  []textFixtureResult `json:"cases"`
	}
	readTextFixture(t, "corpus.json", &corpus)
	readTextFixture(t, "expected.json", &expected)
	if corpus.Schema != 1 || expected.Schema != 1 || len(corpus.Cases) == 0 || len(corpus.Cases) != len(expected.Cases) {
		t.Fatal("inconsistent FTS5 fixture schema")
	}
	var baseline, actual map[string]any
	readTextFixture(t, "manifest.json", &baseline)
	if err := json.Unmarshal(expected.Oracle, &actual); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(baseline)
	if string(a) != string(b) {
		t.Fatal("fixtures differ from pinned SQLite manifest")
	}
	seen := map[string]bool{}
	for i, c := range corpus.Cases {
		r := expected.Cases[i]
		if c.Name == "" || seen[c.Name] || c.Name != r.Name || len(c.Fields) == 0 {
			t.Fatal("invalid FTS5 fixture case", i)
		}
		seen[c.Name] = true
		for _, d := range c.Documents {
			if d.ID == "" || len(d.Values) != len(c.Fields) {
				t.Fatal("invalid fixture document", c.Name)
			}
		}
		if !r.InvalidDefinition && len(c.Queries) != len(r.Queries) {
			t.Fatal("incomplete fixture queries", c.Name)
		}
		for j, q := range r.Queries {
			if q.Query != c.Queries[j] {
				t.Fatal("fixture query order", c.Name)
			}
		}
		for _, token := range r.Tokens {
			if _, err := hex.DecodeString(token.Hex); err != nil || token.Document < 1 || token.Document > len(c.Documents) || token.Offset < 0 {
				t.Fatal("invalid fixture token", c.Name)
			}
		}
	}
	return corpus.Cases, expected.Cases
}

func TestTextFixtureSchema(t *testing.T) { loadTextFixtures(t) }
