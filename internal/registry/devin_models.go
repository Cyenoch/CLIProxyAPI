package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

//go:embed models/devin_models.json
var embeddedDevinModelsJSON []byte

type devinModelsFilePayload struct {
	Devin []*ModelInfo `json:"devin,omitempty"`
}

type devinModelsStore struct {
	mu     sync.RWMutex
	models []*ModelInfo
}

var devinCatalogStore = &devinModelsStore{}

func init() {
	if _, err := loadDevinModelsFromBytes(embeddedDevinModelsJSON, "embed"); err != nil {
		log.Warnf("registry: failed to parse embedded devin_models.json (Devin models unavailable until a valid refresh): %v", err)
	}
}

// GetDevinModels returns a copy of the active catalog, initially loaded from the embedded snapshot.
func GetDevinModels() []*ModelInfo {
	devinCatalogStore.mu.RLock()
	defer devinCatalogStore.mu.RUnlock()
	return cloneModelInfos(devinCatalogStore.models)
}

// LookupDevinModel looks up a model definition from the active Devin catalog.
// Accepts both namespaced ("devin/model") and bare ("model") IDs.
func LookupDevinModel(modelID string) *ModelInfo {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	clean = strings.TrimPrefix(clean, "devin/")
	if clean == "" {
		return nil
	}

	devinCatalogStore.mu.RLock()
	models := devinCatalogStore.models
	devinCatalogStore.mu.RUnlock()

	for _, m := range models {
		mClean := strings.ToLower(strings.TrimPrefix(m.ID, "devin/"))
		if mClean == clean {
			return cloneModelInfo(m)
		}
	}
	return nil
}

func loadDevinModelsFromBytes(data []byte, source string) (bool, error) {
	models, err := ValidateDevinModelsJSON(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", source, err)
	}

	devinCatalogStore.mu.Lock()
	if reflect.DeepEqual(devinCatalogStore.models, models) {
		devinCatalogStore.mu.Unlock()
		return false, nil
	}
	devinCatalogStore.models = models
	devinCatalogStore.mu.Unlock()

	return true, nil
}

// ValidateDevinModelsJSON validates the {"devin": [...]} catalog produced by fetch_devin_models.
func ValidateDevinModelsJSON(data []byte) ([]*ModelInfo, error) {
	var payload devinModelsFilePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode Devin catalog: %w", err)
	}
	if len(payload.Devin) == 0 {
		return nil, fmt.Errorf("Devin catalog must contain a non-empty devin array")
	}
	return sanitizeAndValidateDevinModels(payload.Devin)
}

func sanitizeAndValidateDevinModels(models []*ModelInfo) ([]*ModelInfo, error) {
	seen := make(map[string]struct{}, len(models))
	out := make([]*ModelInfo, 0, len(models))

	for i, m := range models {
		if m == nil {
			return nil, fmt.Errorf("model at index %d is null", i)
		}
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return nil, fmt.Errorf("model at index %d has empty id", i)
		}
		// Automatically namespace model IDs under devin/ if not already prefixed
		if !strings.HasPrefix(strings.ToLower(id), "devin/") {
			id = "devin/" + id
		}
		id = strings.ToLower(id)
		m.ID = id
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate model id: %q", id)
		}
		seen[id] = struct{}{}

		// Ensure proper default fields
		if m.Type == "" {
			m.Type = "devin"
		}
		if m.Object == "" {
			m.Object = "model"
		}
		if len(m.SupportedInputModalities) == 0 {
			m.SupportedInputModalities = []string{"text"}
		}
		if len(m.SupportedOutputModalities) == 0 {
			m.SupportedOutputModalities = []string{"text"}
		}
		if m.InputTokenLimit == 0 && m.ContextLength > 0 {
			m.InputTokenLimit = m.ContextLength
		}
		if m.OutputTokenLimit == 0 && m.MaxCompletionTokens > 0 {
			m.OutputTokenLimit = m.MaxCompletionTokens
		}
		if len(m.SupportedGenerationMethods) == 0 {
			m.SupportedGenerationMethods = []string{"generateContent", "countTokens"}
		}
		out = append(out, m)
	}

	return out, nil
}
