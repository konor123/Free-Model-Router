package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTypedClientReadsDesktopContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_fmr/status":
			_, _ = w.Write([]byte(`{"apiVersion":"1","instanceId":"i"}`))
		case "/_fmr/models":
			_, _ = w.Write([]byte(`{"catalogRevision":4,"data":[]}`))
		case "/_fmr/model-pool":
			_, _ = w.Write([]byte(`{"revision":2,"mode":"Manual","selectedProviderModelIds":[]}`))
		case "/_fmr/logs":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "secret")
	if status, err := client.Status(t.Context()); err != nil || status.InstanceID != "i" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if models, err := client.Models(t.Context()); err != nil || models.CatalogRevision != 4 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	if pool, err := client.ModelPool(t.Context()); err != nil || pool.Revision != 2 {
		t.Fatalf("pool = %+v, %v", pool, err)
	}
	if logs, err := client.Logs(t.Context(), nil); err != nil || len(logs.Data) != 0 {
		t.Fatalf("logs = %+v, %v", logs, err)
	}
}
