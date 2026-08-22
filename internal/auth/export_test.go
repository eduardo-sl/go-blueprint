package auth

import "testing"

// SetBcryptCostForTest lowers the password hashing cost for one test and
// regenerates the placeholder hash, so the two Login paths keep matching costs.
// Timing comparisons need many hashes, and at the production cost of 12 a
// hundred of them takes minutes.
//
// This file is compiled only into the package's own tests, so the hook cannot
// be reached from production code. It mutates package state: a test using it
// must not call t.Parallel.
func SetBcryptCostForTest(t *testing.T, cost int) {
	t.Helper()

	origCost, origHash := _bcryptCost, _dummyHash
	t.Cleanup(func() { _bcryptCost, _dummyHash = origCost, origHash })

	_bcryptCost = cost
	_dummyHash = mustHash(_placeholderPassword)
}
