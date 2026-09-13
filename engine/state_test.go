package engine

import "testing"

// The phase tables are indexed under regPhaseMask, so a phase the compiler cannot rule out folds back into the
// table instead of panicking. That is only safe while every entry it can fold onto refuses the input: rErr for the
// three transition tables, 0 for regClose, which means "cannot close". Those are the zero values today, so the
// property holds by construction -- and this test is what keeps it holding if rErr ever stops being zero.
func TestMaskedPhasesFoldOntoRefusingEntries(t *testing.T) {
	for ph := regPhase(int(rAComma) + 1); ph <= regPhaseMask; ph++ {
		if got := regAfterStr[ph&regPhaseMask]; got != rErr {
			t.Errorf("regAfterStr[%d] = %d, want rErr", ph, got)
		}
		if got := regAfterVal[ph&regPhaseMask]; got != rErr {
			t.Errorf("regAfterVal[%d] = %d, want rErr", ph, got)
		}
		if got := regAfterComma[ph&regPhaseMask]; got != rErr {
			t.Errorf("regAfterComma[%d] = %d, want rErr", ph, got)
		}
		if got := regClose[ph&regPhaseMask]; got != 0 {
			t.Errorf("regClose[%d] = %d, want 0 (cannot close)", ph, got)
		}
	}
}
