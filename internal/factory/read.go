package factory

import (
	"encoding/json"
	"fmt"

	"github.com/mitkox/esf/internal/artifacts"
)

// ReadManifest loads a run's durable manifest from the artifact store.
//
// This is the authoritative record of a finished run: it survives sandbox
// destruction, process restarts and Temporal history retention.
func ReadManifest(factory artifacts.Factory, runID string) (RunManifest, error) {
	store, err := factory.ForRun(runID)
	if err != nil {
		return RunManifest{}, err
	}
	data, err := store.Read(ArtifactRun)
	if err != nil {
		return RunManifest{}, fmt.Errorf("read manifest for run %s: %w", runID, err)
	}
	var manifest RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RunManifest{}, fmt.Errorf("decode manifest for run %s: %w", runID, err)
	}
	return manifest, nil
}

// ReadArtifact loads one artifact from a run.
func ReadArtifact(factory artifacts.Factory, runID, relPath string) ([]byte, error) {
	store, err := factory.ForRun(runID)
	if err != nil {
		return nil, err
	}
	return store.Read(relPath)
}
