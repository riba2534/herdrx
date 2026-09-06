package secure

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, "correct-horse-battery-staple") {
		t.Fatal("valid password was rejected")
	}
	if VerifyPassword(encoded, "wrong-password-value") {
		t.Fatal("invalid password was accepted")
	}
}

func TestPasswordMinimumLength(t *testing.T) {
	if _, err := HashPassword("too-short"); err == nil {
		t.Fatal("short password was accepted")
	}
}
