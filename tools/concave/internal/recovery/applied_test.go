package recovery_test

import (
	"concave/internal/recovery"
	"testing"
)

func TestAppliedValidatesPatchAfterHeadAdvances(t *testing.T) {
	x := setup(t)
	p := x.plan(t)
	if err := recovery.ValidateApplied(x.r, x.c, p); err == nil {
		t.Fatal("unapplied accepted")
	}
	if err := recovery.Apply(x.r, x.c, p); err != nil {
		t.Fatal(err)
	}
	x.f.Write("peer.txt", "peer")
	x.f.Commit()
	if err := recovery.ValidateApplied(x.r, x.c, p); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Validate(x.r, x.c, p); err == nil {
		t.Fatal("old applicability accepted")
	}
	p.Files["crates/core/lib.rs"].Before.SHA256 = "tampered"
	if err := recovery.ValidateApplied(x.r, x.c, p); err == nil {
		t.Fatal("tampered preimage accepted")
	}
}

func TestAppliedRejectsLaterSourceChange(t *testing.T) {
	x := setup(t)
	p := x.plan(t)
	if err := recovery.Apply(x.r, x.c, p); err != nil {
		t.Fatal(err)
	}
	x.f.Write("crates/core/lib.rs", "later edit\n")
	if err := recovery.ValidateApplied(x.r, x.c, p); err == nil {
		t.Fatal("later edit accepted")
	}
}
