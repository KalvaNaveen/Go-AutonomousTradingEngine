package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"bnf_go_engine/model"
	"bnf_go_engine/store"
)

// seedStore opens a throwaway SQLite store with two closed trades on
// different dates and returns it with their IDs.
func seedStore(t *testing.T) (*store.Store, []int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	var ids []int64
	for _, d := range []string{"2026-07-07", "2026-07-08"} {
		p := &model.Position{
			Symbol: "TEST", Qty: 10, EntryPrice: 100, EntryTime: time.Now(),
			SLPrice: 98, TradeDate: d, Paper: true, Status: model.StatusOpen,
		}
		if err := st.SaveOpen(p); err != nil {
			t.Fatalf("seed: %v", err)
		}
		ids = append(ids, p.ID)
	}
	return st, ids
}

func del(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, nil))
	return rec
}

func TestDeleteByID(t *testing.T) {
	st, ids := seedStore(t)
	h := New(st, true).Handler()

	if rec := del(t, h, "/api/delete?id="+itoa(ids[0])); rec.Code != http.StatusOK {
		t.Fatalf("delete id: got %d: %s", rec.Code, rec.Body)
	}
	open, _ := st.OpenPositions()
	if len(open) != 1 || open[0].ID != ids[1] {
		t.Fatalf("expected only second position left, got %+v", open)
	}
	// Deleting again → 404.
	if rec := del(t, h, "/api/delete?id="+itoa(ids[0])); rec.Code != http.StatusNotFound {
		t.Fatalf("re-delete: want 404, got %d", rec.Code)
	}
}

func TestDeleteByDate(t *testing.T) {
	st, _ := seedStore(t)
	h := New(st, true).Handler()

	if rec := del(t, h, "/api/delete?date=2026-07-07"); rec.Code != http.StatusOK {
		t.Fatalf("delete date: got %d: %s", rec.Code, rec.Body)
	}
	if pos, _ := st.PositionsByDate("2026-07-07"); len(pos) != 0 {
		t.Fatalf("2026-07-07 rows still present: %+v", pos)
	}
	if pos, _ := st.PositionsByDate("2026-07-08"); len(pos) != 1 {
		t.Fatalf("2026-07-08 rows should be untouched, got %+v", pos)
	}
}

func TestDeleteGuards(t *testing.T) {
	st, ids := seedStore(t)
	srv := New(st, true)
	srv.SetTrigger("sekrit", func(bool) string { return "" })
	h := srv.Handler()

	// GET refused.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/delete?id=1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: want 405, got %d", rec.Code)
	}
	// Wrong/missing token refused.
	if rec := del(t, h, "/api/delete?id="+itoa(ids[0])); rec.Code != http.StatusForbidden {
		t.Fatalf("no token: want 403, got %d", rec.Code)
	}
	// Correct token works.
	if rec := del(t, h, "/api/delete?token=sekrit&id="+itoa(ids[0])); rec.Code != http.StatusOK {
		t.Fatalf("with token: got %d: %s", rec.Code, rec.Body)
	}
	// Neither id nor date → 400.
	if rec := del(t, h, "/api/delete?token=sekrit"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no args: want 400, got %d", rec.Code)
	}
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }
