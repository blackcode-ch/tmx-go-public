package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOwnerNamesConfig(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"owner":{"display_name":"ExampleOwner","aliases":["Alias.One"]}}`), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.Owner.Names(); !reflect.DeepEqual(got, []string{"ExampleOwner", "Alias.One"}) {
		t.Fatalf("names: %v", got)
	}
	if got := (Config{}).Owner.Names(); len(got) != 0 {
		t.Fatalf("default names: %v", got)
	}
}
