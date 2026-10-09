package auth

import (
	"testing"
)

type droppingKeyring struct{ memKeyring }

func (k droppingKeyring) Set(string, string, string) error { return nil }

func TestNtfyTokenStorageVerified(t *testing.T) {
	if err := StoreNtfyToken(droppingKeyring{memKeyring{}}, "TEST_TOKEN"); err == nil {
		t.Fatal("token was not persisted but storage claimed success")
	}
}

func TestNtfyTokenResolution(t *testing.T) {
	kr := memKeyring{}
	t.Setenv("BAMBU_NTFY_TOKEN", "")
	if err := StoreNtfyToken(kr, "TEST_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if NtfyToken(kr) != "TEST_TOKEN" {
		t.Fatal("keychain token not resolved")
	}
	t.Setenv("BAMBU_NTFY_TOKEN", "ENV_TOKEN")
	if NtfyToken(kr) != "ENV_TOKEN" {
		t.Fatal("environment must take precedence")
	}
}
