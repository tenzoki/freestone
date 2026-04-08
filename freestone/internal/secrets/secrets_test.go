package secrets

import (
	"os"
	"testing"
)

func TestEnvResolver_EmptyRef(t *testing.T) {
	r := &EnvResolver{}
	_, err := r.Resolve("")
	if err == nil {
		t.Fatal("expected error for empty ref")
	}
}

func TestEnvResolver_MissingEnv(t *testing.T) {
	r := &EnvResolver{}
	_, err := r.Resolve("nonexistent-secret-key-xyz")
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestEnvResolver_ValidEnv(t *testing.T) {
	const ref = "test-secret-key"
	const want = "s3cret"
	os.Setenv("TEST_SECRET_KEY", want)
	defer os.Unsetenv("TEST_SECRET_KEY")

	r := &EnvResolver{}
	got, err := r.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEnvResolver_HyphenToUnderscore(t *testing.T) {
	os.Setenv("ICLOUD_CAL_PW", "pw123")
	defer os.Unsetenv("ICLOUD_CAL_PW")

	r := &EnvResolver{}
	got, err := r.Resolve("icloud-cal-pw")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pw123" {
		t.Errorf("got %q", got)
	}
}

func TestNew_EnvOverride(t *testing.T) {
	os.Setenv("FREESTONE_SECRETS", "env")
	defer os.Unsetenv("FREESTONE_SECRETS")

	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*EnvResolver); !ok {
		t.Errorf("expected *EnvResolver, got %T", r)
	}
}

func TestNew_DefaultDarwin(t *testing.T) {
	os.Unsetenv("FREESTONE_SECRETS")

	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// On Darwin we expect KeychainResolver.
	if _, ok := r.(*KeychainResolver); !ok {
		t.Errorf("expected *KeychainResolver, got %T", r)
	}
}
