package errfmt

import (
	"errors"
	"testing"
)

func TestWrapAndAs(t *testing.T) {
	base := errors.New("connection refused")
	err := Wrap(ExitRetryable, base, "cannot reach printer").WithHint("check the host")
	var wrapped error = err
	got := As(wrapped)
	if got.Code != ExitRetryable || got.Name != "retryable" || got.Hint != "check the host" {
		t.Fatalf("unexpected: %+v", got)
	}
	if !errors.Is(wrapped, base) {
		t.Fatal("cause not unwrapped")
	}
	if got.Error() != "cannot reach printer: connection refused" {
		t.Fatalf("Error() = %q", got.Error())
	}
}

func TestAsPlainError(t *testing.T) {
	got := As(errors.New("boom"))
	if got.Code != ExitError || got.Message != "boom" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestTableUnique(t *testing.T) {
	seen := map[int]bool{}
	for _, e := range Table() {
		if seen[e.Code] {
			t.Fatalf("duplicate code %d", e.Code)
		}
		seen[e.Code] = true
		if Name(e.Code) != e.Name {
			t.Fatalf("Name(%d) mismatch", e.Code)
		}
	}
}
