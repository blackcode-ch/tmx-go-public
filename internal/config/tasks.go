package config

// TaskConfig controls observation windows in seconds. Nonpositive values use defaults.
type TaskConfig struct {
	StartSeconds  int `json:"start_seconds,omitempty"`
	FinishSeconds int `json:"finish_seconds,omitempty"`
}

func (c *TaskConfig) Windows() (start, finish float64) {
	start, finish = 900, 86400
	if c != nil {
		if c.StartSeconds > 0 {
			start = float64(c.StartSeconds)
		}
		if c.FinishSeconds > 0 {
			finish = float64(c.FinishSeconds)
		}
	}
	return
}
