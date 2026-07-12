package history

import (
	"testing"
	"time"
)

func TestParseJSONL(t *testing.T) {
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	raw := `{"store":"mercadona","productId":"1","productName":"Leche","qty":2,"unitPrice":1.1,"timestamp":"2026-03-01T12:00:00Z"}
{"store":"mercadona","productId":"2","productName":"Huevos","qty":1,"unitPrice":2.5,"timestamp":"2026-03-02T08:00:00Z"}
`
	got, err := ParseJSONL([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d want 2", len(got))
	}
	if got[0].ProductID != "1" || got[0].Qty != 2 || !got[0].Timestamp.Equal(ts) {
		t.Errorf("first record = %+v", got[0])
	}
}

func TestAggregate(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	records := []Record{
		{Store: "mercadona", ProductID: "a", ProductName: "Leche", Qty: 2, Timestamp: t1},
		{Store: "mercadona", ProductID: "a", ProductName: "Leche entera", Qty: 1, Timestamp: t2},
		{Store: "mercadona", ProductID: "a", ProductName: "Leche", Qty: 3, Timestamp: t3},
		{Store: "mercadona", ProductID: "b", ProductName: "Café", Qty: 1, Timestamp: t2},
		{Store: "consum", ProductID: "c", ProductName: "Pan", Qty: 2, Timestamp: t1},
		{Store: "consum", ProductID: "c", ProductName: "Pan", Qty: 2, Timestamp: t2},
	}

	for _, tc := range []struct {
		name   string
		store  string
		min    int
		want   int
		topID  string
		topQty float64
		topN   int
		last   string
	}{
		{"all min2", "", 2, 2, "a", 2, 3, "2026-03-01"},
		{"mercadona min2", "mercadona", 2, 1, "a", 2, 3, "2026-03-01"},
		{"mercadona min3 drops cafe", "mercadona", 3, 1, "a", 2, 3, "2026-03-01"},
		{"consum min2", "consum", 2, 1, "c", 2, 2, "2026-02-01"},
		{"none min5", "", 5, 0, "", 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Aggregate(records, tc.store, tc.min)
			if len(got) != tc.want {
				t.Fatalf("len = %d want %d: %+v", len(got), tc.want, got)
			}
			if tc.want == 0 {
				return
			}
			if got[0].ProductID != tc.topID {
				t.Errorf("top id = %q want %q", got[0].ProductID, tc.topID)
			}
			if got[0].TypicalQty != tc.topQty {
				t.Errorf("typical qty = %v want %v", got[0].TypicalQty, tc.topQty)
			}
			if got[0].TimesBought != tc.topN {
				t.Errorf("times = %d want %d", got[0].TimesBought, tc.topN)
			}
			if got[0].LastBought != tc.last {
				t.Errorf("last = %q want %q", got[0].LastBought, tc.last)
			}
		})
	}
}

func TestMedianQty(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want float64
	}{{[]float64{1, 3, 5}, 3}, {[]float64{1, 2, 3, 4}, 2.5}, {nil, 1}} {
		if got := medianQty(tc.in); got != tc.want {
			t.Errorf("medianQty(%v) = %v want %v", tc.in, got, tc.want)
		}
	}
}
