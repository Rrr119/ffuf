package ffuf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SimilarityThreshold is a flag/config value that accepts both the CLI string
// form and either a string or numeric value in a TOML config file. Explicit
// records whether the user supplied it, which lets -rct imply -rc without
// treating the default as an explicit request.
type SimilarityThreshold struct {
	Value    float64
	Explicit bool
}

// String implements flag.Value.
func (t SimilarityThreshold) String() string {
	return strconv.FormatFloat(t.Value, 'f', -1, 64)
}

// Set implements flag.Value.
func (t *SimilarityThreshold) Set(value string) error {
	parsed, err := parseSimilarityThreshold(value)
	if err != nil {
		return err
	}
	t.Value = parsed
	t.Explicit = true
	return nil
}

// UnmarshalTOML accepts a numeric or string threshold from a config file.
func (t *SimilarityThreshold) UnmarshalTOML(value interface{}) error {
	switch v := value.(type) {
	case float64:
		return t.setParsed(v)
	case int64:
		return t.setParsed(float64(v))
	case int:
		return t.setParsed(float64(v))
	case string:
		return t.Set(v)
	default:
		return fmt.Errorf("response cluster threshold must be a number or string, got %T", value)
	}
}

// MarshalJSON keeps the flag/config value round-trippable through FFUFHASH
// history.
func (t SimilarityThreshold) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Value    float64 `json:"value"`
		Explicit bool    `json:"set"`
	}{Value: t.Value, Explicit: t.Explicit})
}

// UnmarshalJSON restores both the numeric threshold and whether it was
// explicitly supplied.
func (t *SimilarityThreshold) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '{' {
		var value struct {
			Value    float64 `json:"value"`
			Explicit bool    `json:"set"`
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err := t.setParsed(value.Value); err != nil {
			return err
		}
		t.Explicit = value.Explicit
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		return t.Set(value)
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return t.setParsed(value)
}

func (t *SimilarityThreshold) setParsed(value float64) error {
	if value <= 0 || value > 1 {
		return fmt.Errorf("response cluster threshold must be greater than 0 and at most 1, got %v", value)
	}
	t.Value = value
	t.Explicit = true
	return nil
}

func parseSimilarityThreshold(value string) (float64, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 0 || parsed > 1 {
		return 0, fmt.Errorf("response cluster threshold must be greater than 0 and at most 1, got %q", value)
	}
	return parsed, nil
}
