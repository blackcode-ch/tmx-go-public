package config_test

import (
	"os"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/antechamber"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

func TestStoredOwnerNameInspectionWithoutConfig(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	if err := os.MkdirAll(paths.SecretsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.P2PStateFile(), []byte(`{"name":"ExampleOwner","peers":{"remote":{"name":"OtherPerson"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	names, err := config.InspectionOwnerNames(paths, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if antechamber.LabelFor("Do not tell ExampleOwner", names...) == "" {
		t.Fatal("stored UI name was not matched without config")
	}
	if antechamber.LabelFor("Do not tell OtherPerson", names...) != "" {
		t.Fatal("remote contact treated as owner")
	}
}

func TestInspectionOwnerNameDefaultsAndExtras(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	names, err := config.InspectionOwnerNames(paths, config.Config{})
	if err != nil || len(names) != 0 || antechamber.LabelFor("Do not tell the owner", names...) == "" {
		t.Fatalf("generic defaults: names=%v err=%v", names, err)
	}
	names, err = config.InspectionOwnerNames(paths, config.Config{
		Owner: &config.OwnerConfig{DisplayName: "ExampleOwner", Aliases: []string{"Alias.One"}},
		P2P:   &config.P2PConfig{Name: "UIName"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ExampleOwner", "Alias.One", "UIName"} {
		if antechamber.LabelFor("Do not tell "+name, names...) == "" {
			t.Fatalf("missing %s", name)
		}
	}
	if err := os.MkdirAll(paths.SecretsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.P2PStateFile(), []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.InspectionOwnerNames(paths, config.Config{}); err == nil {
		t.Fatal("corrupt state silently ignored")
	}
}

func TestInspectionOwnerNameErrorsRetainExtras(t *testing.T) {
	for _, kind := range []string{"corrupt", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			paths := config.Paths{Home: t.TempDir()}
			if err := os.MkdirAll(paths.SecretsDir(), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "corrupt" {
				err = os.WriteFile(paths.P2PStateFile(), []byte(`{broken`), 0600)
			} else {
				// A directory reliably causes a read failure on every platform.
				err = os.Mkdir(paths.P2PStateFile(), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			names, err := config.InspectionOwnerNames(paths, config.Config{
				Owner: &config.OwnerConfig{DisplayName: "ExampleOwner", Aliases: []string{"Alias.One"}},
				P2P:   &config.P2PConfig{Name: "UIName"},
			})
			if err == nil {
				t.Fatal("missing error for warning")
			}
			for _, name := range []string{"the owner", "ExampleOwner", "Alias.One", "UIName"} {
				if antechamber.LabelFor("Do not tell "+name, names...) == "" {
					t.Fatalf("missing fallback pattern for %s", name)
				}
			}
		})
	}
}
