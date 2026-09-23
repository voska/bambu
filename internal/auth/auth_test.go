package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
)

type memKeyring map[string]string

func (m memKeyring) Get(s, u string) (string, error) {
	if v, ok := m[s+"/"+u]; ok {
		return v, nil
	}
	return "", ErrNotFound
}
func (m memKeyring) Set(s, u, p string) error { m[s+"/"+u] = p; return nil }
func (m memKeyring) Delete(s, u string) error {
	if _, ok := m[s+"/"+u]; !ok {
		return ErrNotFound
	}
	delete(m, s+"/"+u)
	return nil
}

func TestResolveOrder(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "BambuStudio.conf")
	_ = os.WriteFile(conf, []byte(`{"access_code":{"SER1":"studio11"},"user_access_code":{"SER2":"studio22"}}`+"\nchecksum 123\n"), 0o600)
	t.Setenv("BAMBU_STUDIO_CONF", conf)
	t.Setenv("BAMBU_ACCESS_CODE", "")

	kr := memKeyring{}
	if c, src, _ := Resolve(kr, "SER1"); c != "studio11" || src != SourceBambuStudio {
		t.Fatalf("studio: %s %s", c, src)
	}
	if c, src, _ := Resolve(kr, "SER2"); c != "studio22" || src != SourceBambuStudio {
		t.Fatalf("studio user_access_code: %s %s", c, src)
	}
	t.Setenv("BAMBU_ACCESS_CODE", "envcode1")
	if c, src, _ := Resolve(kr, "SER1"); c != "envcode1" || src != SourceEnv {
		t.Fatalf("env: %s %s", c, src)
	}
	if err := Store(kr, "SER1", "keych123"); err != nil {
		t.Fatal(err)
	}
	if c, src, _ := Resolve(kr, "SER1"); c != "keych123" || src != SourceKeychain {
		t.Fatalf("keychain: %s %s", c, src)
	}
	if err := Remove(kr, "SER1"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(kr, "SER1"); err != nil {
		t.Fatal("remove must be idempotent")
	}
	t.Setenv("BAMBU_ACCESS_CODE", "")
	if _, _, err := Resolve(kr, "SER9"); errfmt.As(err).Code != errfmt.ExitAuth {
		t.Fatalf("want auth error, got %v", err)
	}
}

func TestValidateCode(t *testing.T) {
	for _, bad := range []string{"", "abc", "has space1", "semi;colon", "x\x01yyyyy"} {
		if ValidateCode(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if ValidateCode("12345678") != nil || ValidateCode("aB3dE6gH") != nil {
		t.Error("rejected valid code")
	}
}
