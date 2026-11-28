package model

// AccountUsageReading is the local account service's observation for head-mail
// generation. It omits account credentials and personal identity details.
type AccountUsageReading struct {
	Harness, Name string
	IdentityKey   [32]byte
	Connected     bool
	Refreshing    bool
	QuotaKnown    bool
	CheckedAt     int64
	Limits        []AccountUsageLimit
}

type AccountUsageLimit struct {
	ID, Label   string
	UsedPercent float64
	ResetsAt    int64
	FreeResets  int // -1 when the provider has no current count
}
