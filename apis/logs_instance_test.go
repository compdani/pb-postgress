package apis_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/dbx"
)

func TestLogsAPIInstanceIsolation(t *testing.T) {
	s := newOAuth2TestServer(t)
	instanceID, err := s.app.InstanceID()
	if err != nil {
		t.Fatal(err)
	}
	const adminToken = "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InN5d2JoZWNuaDQ2cmhtMCIsInR5cGUiOiJhdXRoIiwiY29sbGVjdGlvbklkIjoicGJjXzMxNDI2MzU4MjMiLCJleHAiOjI1MjQ2MDQ0NjEsInJlZnJlc2hhYmxlIjp0cnVlfQ.UXgO3j-0BumcugrFjbd7j0M4MQvbrLggLlcu_YNGjoY"
	for _, row := range []dbx.Params{
		{"id": "local-log", "instanceId": instanceID},
		{"id": "foreign-log", "instanceId": "other-instance"},
		{"id": "legacy-log", "instanceId": ""},
	} {
		row["created"] = "2022-05-01 10:00:00.123Z"
		if _, err := s.app.AuxDB().Insert("_logs", row).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/logs", "/api/logs?filter=instanceId='other-instance'||id='local-log'"} {
		status, data := s.jsonRequest(http.MethodGet, path, nil, adminToken)
		if status != 200 || data["totalItems"] != float64(1) {
			t.Fatalf("list escaped scope: %d %v", status, data)
		}
	}
	for _, id := range []string{"foreign-log", "legacy-log"} {
		status, _ := s.jsonRequest(http.MethodGet, "/api/logs/"+id, nil, adminToken)
		if status != 404 {
			t.Fatalf("view %s: %d", id, status)
		}
	}
	res := s.do(http.MethodGet, "/api/logs/stats", nil, map[string]string{"Authorization": adminToken})
	if res.StatusCode != 200 {
		t.Fatalf("stats: %d", res.StatusCode)
	}
	var stats []struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(res.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Total != 1 {
		t.Fatalf("stats escaped scope: %+v", stats)
	}
	res.Body.Close()
	res = s.do(http.MethodDelete, "/api/logs", nil, map[string]string{"Authorization": adminToken})
	if res.StatusCode != 204 {
		t.Fatalf("truncate: %d", res.StatusCode)
	}
	res.Body.Close()
	var remaining int
	if err := s.app.AuxDB().Select("count(*)").From("_logs").Row(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("truncate affected foreign or legacy logs: %d left", remaining)
	}
}
