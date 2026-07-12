package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Record is one successful cart-add line appended to the local purchase history.
type Record struct {
	Store       string    `json:"store"`
	ProductID   string    `json:"productId"`
	ProductName string    `json:"productName"`
	Qty         float64   `json:"qty"`
	UnitPrice   float64   `json:"unitPrice"`
	Timestamp   time.Time `json:"timestamp"`
}

// Usual is one regularly-bought product aggregated from history.
type Usual struct {
	ProductID   string  `json:"productId"`
	ProductName string  `json:"productName"`
	Store       string  `json:"store"`
	TimesBought int     `json:"timesBought"`
	TypicalQty  float64 `json:"typicalQty"`
	LastBought  string  `json:"lastBought"`
}

// ConfigDir is the per-user state directory (session cache, history, etc.).
func ConfigDir() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return dir
}

func path() string { return filepath.Join(ConfigDir(), "history.jsonl") }

// Append writes one record to the history file. On error it calls warn and returns.
func Append(r Record, warn func(format string, args ...any)) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		warn("history: %v", err)
		return
	}
	f, err := os.OpenFile(path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		warn("history: %v", err)
		return
	}
	defer f.Close()
	b, err := json.Marshal(r)
	if err != nil {
		warn("history: %v", err)
		return
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		warn("history: %v", err)
	}
}

// Load reads every record from the history file. A missing file yields nil, nil.
func Load() ([]Record, error) {
	b, err := os.ReadFile(path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseJSONL(b)
}

// ParseJSONL parses purchase-history lines (for tests and Load).
func ParseJSONL(b []byte) ([]Record, error) {
	var out []Record
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

type groupKey struct {
	store, id string
}

type groupAcc struct {
	name string
	qtys []float64
	last time.Time
}

// Aggregate rolls records into usual items. storeFilter empty means all stores.
func Aggregate(records []Record, storeFilter string, minTimes int) []Usual {
	if minTimes < 1 {
		minTimes = 1
	}
	groups := map[groupKey]*groupAcc{}
	for _, r := range records {
		if storeFilter != "" && r.Store != storeFilter {
			continue
		}
		if r.Store == "" || r.ProductID == "" {
			continue
		}
		k := groupKey{r.Store, r.ProductID}
		g, ok := groups[k]
		if !ok {
			g = &groupAcc{name: r.ProductName}
			groups[k] = g
		}
		if r.ProductName != "" {
			g.name = r.ProductName
		}
		g.qtys = append(g.qtys, r.Qty)
		if r.Timestamp.After(g.last) {
			g.last = r.Timestamp
		}
	}
	var out []Usual
	for k, g := range groups {
		if len(g.qtys) < minTimes {
			continue
		}
		last := ""
		if !g.last.IsZero() {
			last = g.last.UTC().Format("2006-01-02")
		}
		out = append(out, Usual{
			ProductID:   k.id,
			ProductName: g.name,
			Store:       k.store,
			TimesBought: len(g.qtys),
			TypicalQty:  medianQty(g.qtys),
			LastBought:  last,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TimesBought != out[j].TimesBought {
			return out[i].TimesBought > out[j].TimesBought
		}
		if out[i].Store != out[j].Store {
			return out[i].Store < out[j].Store
		}
		return out[i].ProductName < out[j].ProductName
	})
	return out
}

func medianQty(qtys []float64) float64 {
	if len(qtys) == 0 {
		return 1
	}
	sorted := append([]float64(nil), qtys...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
