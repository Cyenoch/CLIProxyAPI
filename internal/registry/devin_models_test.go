package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateDevinModelsJSON(t *testing.T) {
	t.Run("valid envelope devin", func(t *testing.T) {
		data := []byte(`{
			"devin": [
				{
					"id": "devin/swe-2",
					"display_name": "SWE-2",
					"owned_by": "cognition",
					"context_length": 262000
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "devin/swe-2" {
			t.Fatalf("unexpected models: %+v", models)
		}
		if models[0].Type != "devin" {
			t.Errorf("expected type 'devin', got %q", models[0].Type)
		}
	})

	for _, invalid := range []string{`{"models":[{"id":"swe-2"}]}`, `[{"id":"swe-2"}]`} {
		if _, err := ValidateDevinModelsJSON([]byte(invalid)); err == nil {
			t.Errorf("accepted unsupported catalog format: %s", invalid)
		}
	}

	t.Run("clean id without devin prefix automatically namespaced", func(t *testing.T) {
		data := []byte(`{
			"devin": [
				{
					"id": "swe-2",
					"display_name": "SWE-2"
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "devin/swe-2" {
			t.Fatalf("expected auto-namespaced to devin/swe-2, got: %q", models[0].ID)
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		_, err := ValidateDevinModelsJSON([]byte(`   `))
		if err == nil {
			t.Fatal("expected error on empty payload, got nil")
		}
	})

	t.Run("empty model id", func(t *testing.T) {
		data := []byte(`{"devin": [{"id": "", "display_name": "No ID"}]}`)
		_, err := ValidateDevinModelsJSON(data)
		if err == nil {
			t.Fatal("expected error on empty model id, got nil")
		}
	})

	for name, data := range map[string][]byte{
		"exact duplicate": []byte(`{"devin": [{"id": "devin/swe-2"}, {"id": "devin/swe-2"}]}`),
		"case duplicate":  []byte(`{"devin": [{"id": "devin/SWE-2"}, {"id": "devin/swe-2"}]}`),
	} {
		t.Run("duplicate model id/"+name, func(t *testing.T) {
			_, err := ValidateDevinModelsJSON(data)
			if err == nil {
				t.Fatal("expected error on duplicate model id, got nil")
			}
		})
	}
}

func TestEmbeddedDevinModelsLoadedOnStartup(t *testing.T) {
	models := GetDevinModels()
	if len(models) < 30 {
		t.Fatalf("expected at least 30 embedded Devin models, got %d", len(models))
	}

	foundMap := make(map[string]bool)
	for _, m := range models {
		foundMap[m.ID] = true
	}

	expectedIDs := []string{
		"devin/swe-2",
		"devin/glm-5-2",
		"devin/glm-5-3",
		"devin/deepseek-v4-flash",
		"devin/deepseek-v4-1-flash",
		"devin/gemini-3-8-flash",
		"devin/grok-4-6",
		"devin/claude-fable-5-1",
		"devin/gpt-6-astra",
	}

	for _, id := range expectedIDs {
		if !foundMap[id] {
			t.Errorf("expected embedded catalog to contain %q", id)
		}
	}
}

func TestDevinModelsRemoteFetchFallback(t *testing.T) {
	// Test remote failure maintains existing embedded data
	origURLs := devinModelsURLs
	defer func() { devinModelsURLs = origURLs }()

	// Point to failing server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	devinModelsURLs = []string{ts.URL + "/devin_models.json"}

	initialCount := len(GetDevinModels())
	if initialCount == 0 {
		t.Fatal("expected non-empty initial Devin models")
	}

	// Attempt refresh from failing remote
	tryRefreshDevinModels(context.Background(), "test failing refresh")

	afterCount := len(GetDevinModels())
	if afterCount != initialCount {
		t.Fatalf("expected catalog to remain intact with %d models, got %d", initialCount, afterCount)
	}

	// Point to succeeding server with valid update
	validUpdate := []byte(`{
		"devin": [
			{
				"id": "devin/custom-test-model",
				"display_name": "Custom Test Model",
				"owned_by": "custom"
			}
		]
	}`)

	tsValid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(validUpdate)
	}))
	defer tsValid.Close()

	devinModelsURLs = []string{tsValid.URL + "/devin_models.json"}
	tryRefreshDevinModels(context.Background(), "test succeeding refresh")

	updatedModels := GetDevinModels()
	if len(updatedModels) != 1 || updatedModels[0].ID != "devin/custom-test-model" {
		t.Fatalf("expected catalog to be updated to custom-test-model, got: %+v", updatedModels)
	}

	// Restore original embedded data for following tests
	_, _ = loadDevinModelsFromBytes(embeddedDevinModelsJSON, "restore-embed")
}

type devinCatalogTransport struct{}

func (devinCatalogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &devinCatalogBody{ctx: req.Context(), Reader: strings.NewReader(`{"devin":[{"id":"updated"}]}`)}, Request: req}, nil
}

type devinCatalogBody struct {
	ctx context.Context
	*strings.Reader
}

func (b *devinCatalogBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.Reader.Read(p)
}
func (*devinCatalogBody) Close() error { return nil }

func TestDevinCatalogReadsBodyBeforeCancelAndNotifies(t *testing.T) {
	oldTransport := http.DefaultTransport
	oldURLs := devinModelsURLs
	http.DefaultTransport = devinCatalogTransport{}
	devinModelsURLs = []string{"https://catalog.example/devin_models.json"}
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
		devinModelsURLs = oldURLs
		SetModelRefreshCallback(nil)
		_, _ = loadDevinModelsFromBytes(embeddedDevinModelsJSON, "restore")
	})
	var notifications [][]string
	SetModelRefreshCallback(func(providers []string) { notifications = append(notifications, providers) })
	notifications = nil
	tryRefreshDevinModels(context.Background(), "test")
	if model := LookupDevinModel("updated"); model == nil {
		t.Fatal("catalog body was canceled before it could be read")
	}
	if len(notifications) != 1 || len(notifications[0]) != 1 || notifications[0][0] != "devin" {
		t.Fatalf("refresh notifications=%v, want [[devin]]", notifications)
	}
	tryRefreshDevinModels(context.Background(), "unchanged")
	if len(notifications) != 1 {
		t.Fatalf("unchanged catalog triggered refresh: %v", notifications)
	}
}

func TestModelCatalogUpdatersProgressIndependently(t *testing.T) {
	blockedCtx, cancelBlocked := context.WithCancel(context.Background())
	defer cancelBlocked()
	started := make(chan struct{})
	blockedDone := make(chan struct{})
	go func() {
		defer close(blockedDone)
		runModelCatalogUpdater(blockedCtx, func(ctx context.Context, _ string) { close(started); <-ctx.Done() })
	}()
	<-started
	healthyCtx, cancelHealthy := context.WithCancel(context.Background())
	defer cancelHealthy()
	healthyDone := make(chan struct{})
	go func() {
		defer close(healthyDone)
		runModelCatalogUpdater(healthyCtx, func(context.Context, string) { cancelHealthy() })
	}()
	<-healthyDone
	select {
	case <-blockedDone:
		t.Fatal("unrelated refresh was canceled")
	default:
	}
	cancelBlocked()
	<-blockedDone
}
