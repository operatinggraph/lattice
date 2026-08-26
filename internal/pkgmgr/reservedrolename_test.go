package pkgmgr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateNoReservedRoleName_RejectsOperator(t *testing.T) {
	def := Definition{Name: "test-package", Roles: []RoleSpec{{CanonicalName: "operator"}}}
	err := def.validateNoReservedRoleName()
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator")
}

func TestValidateNoReservedRoleName_AllowsOrdinaryNames(t *testing.T) {
	def := Definition{Name: "test-package", Roles: []RoleSpec{{CanonicalName: "consumer"}, {CanonicalName: "staff"}}}
	require.NoError(t, def.validateNoReservedRoleName())
}

func TestValidateNoReservedRoleName_NoRolesIsFine(t *testing.T) {
	def := Definition{Name: "test-package"}
	require.NoError(t, def.validateNoReservedRoleName())
}

// TestValidateAll_RejectsReservedRoleName proves the guard is actually
// REACHED from validateAll — Install/Upgrade/Apply's shared, pre-KV
// pre-flight (preflight calls validateAll as its first real check) — not
// just callable in isolation. Mirrors packagename_test.go's own
// validateAll()-level proof for validatePackageName.
func TestValidateAll_RejectsReservedRoleName(t *testing.T) {
	def := sampleDef("0.1.0")
	def.Roles = append(def.Roles, RoleSpec{CanonicalName: "operator", Description: "smoke"})
	err := def.validateAll()
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator")
}
