package registry

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
)

const maxDevinModelsSize = 8 << 20

var devinModelsURLs = []string{
	"https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/devin_models.json",
	"https://models.router-for.me/devin_models.json",
}

var devinModelsUpdaterOnce sync.Once

// StartDevinModelsUpdater refreshes the edge-local Devin catalog, including in Home mode.
func StartDevinModelsUpdater(ctx context.Context) {
	devinModelsUpdaterOnce.Do(func() { go runModelCatalogUpdater(ctx, tryRefreshDevinModels) })
}

func tryRefreshDevinModels(ctx context.Context, label string) {
	data, sourceURL := fetchDevinModelsFromRemote(ctx)
	if data == nil {
		log.Warnf("%s: fetch failed from all URLs, keeping current data (embedded or cached fallback)", label)
		return
	}

	changed, err := loadDevinModelsFromBytes(data, sourceURL)
	if err != nil {
		log.Warnf("%s: fetched catalog rejected, keeping current data: %v", label, err)
		return
	}
	if !changed {
		log.Infof("%s completed from %s, no changes detected", label, sourceURL)
		return
	}
	log.Infof("%s completed from %s, catalog updated", label, sourceURL)
	notifyModelRefresh([]string{"devin"})
}

func fetchDevinModelsFromRemote(ctx context.Context) ([]byte, string) {
	for _, sourceURL := range devinModelsURLs {
		body, err := fetchModelCatalog(ctx, sourceURL, maxDevinModelsSize)
		if err != nil {
			log.Warnf("devin models updater: fetch failed from %s: %v", sourceURL, err)
			continue
		}
		if _, errValidate := ValidateDevinModelsJSON(body); errValidate != nil {
			log.Warnf("devin models updater: invalid catalog from %s: %v", sourceURL, errValidate)
			continue
		}
		return body, sourceURL
	}
	return nil, ""
}
