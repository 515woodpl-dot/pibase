package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSQLChosenHTTPStatus(t *testing.T) {
	w := httptest.NewRecorder()
	writeAPIError(w, &pgconn.PgError{Code: "PT409", Message: "order changed", Hint: "reload the order"})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "order changed") || !strings.Contains(w.Body.String(), "reload the order") {
		t.Fatalf("PT409 response: %d %s", w.Code, w.Body.String())
	}
}
