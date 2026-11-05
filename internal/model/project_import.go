package model

import "encoding/json"

// ImportOrigin distinguishes migrated source data from a new owner action.
type ImportOrigin struct {
	Source       string `json:"source"`
	ID           string `json:"id"`
	ImportedBy   string `json:"imported_by"`
	ImportedAt   int64  `json:"imported_at"`
	SourceAuthor string `json:"source_author,omitempty"`
	SourceState  string `json:"source_state,omitempty"`
}

// ProjectImportReceipt retains the accepted source snapshot and stable card IDs.
type ProjectImportReceipt struct {
	ProjectID  string          `json:"project_id"`
	Source     string          `json:"source"`
	ID         string          `json:"id"`
	SHA256     string          `json:"sha256"`
	ImportedBy string          `json:"imported_by"`
	ImportedAt int64           `json:"imported_at"`
	CardIDs    []string        `json:"card_ids"`
	Snapshot   json.RawMessage `json:"snapshot"`
}
