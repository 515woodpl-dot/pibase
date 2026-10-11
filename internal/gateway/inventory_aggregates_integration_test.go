package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestDataAPIAggregatesRespectRLS(t *testing.T) {
	e := newAuditEnv(t, nil)
	owner := e.createUser("aggregate-owner@example.test")
	other := e.createUser("aggregate-other@example.test")
	e.mustSQL(`CREATE TABLE inventory_amounts (id int PRIMARY KEY,owner_id uuid NOT NULL,category text NOT NULL,amount int NOT NULL);
ALTER TABLE inventory_amounts ENABLE ROW LEVEL SECURITY;
GRANT SELECT ON inventory_amounts TO authenticated;
CREATE POLICY inventory_amounts_read ON inventory_amounts FOR SELECT TO authenticated USING (owner_id=auth.uid());
INSERT INTO inventory_amounts VALUES (1,'` + owner + `','tools',5),(2,'` + owner + `','tools',7),(3,'` + other + `','tools',100);`)
	token := e.login("aggregate-owner@example.test").Access
	body := requestAuthJSONBearer(t, e.h.Client(), http.MethodGet,
		e.h.URL+"/rest/v1/inventory_amounts?select=category,count(),total:amount.sum()&category=eq.tools",
		e.p.AnonKey, token, "", 200)
	var rows []struct {
		Count int `json:"count"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 2 || rows[0].Total != 12 {
		t.Fatalf("aggregate included another user's rows: %s", body)
	}
}
