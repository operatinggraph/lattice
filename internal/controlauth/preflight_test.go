package controlauth

import (
	"context"
	"errors"
	"testing"
)

func TestPreflight_StubModeSkipsEntirely(t *testing.T) {
	alerts := &fakeAlerts{}
	c := newTestChecker(&fakeReader{}, AuthModeStub, alerts)
	Preflight(context.Background(), c, "vtx.identity.OP", nil)
	if len(alerts.calls) != 0 {
		t.Fatalf("stub mode preflight must be a no-op, got alerts %v", alerts.calls)
	}
}

func TestPreflight_NoConfiguredOperatorSkipsWithoutAlert(t *testing.T) {
	alerts := &fakeAlerts{}
	c := newTestChecker(&fakeReader{}, AuthModeCapability, alerts)
	Preflight(context.Background(), c, "", nil)
	if len(alerts.calls) != 0 {
		t.Fatalf("an unconfigured operator must not alert (nothing to check), got %v", alerts.calls)
	}
}

func TestPreflight_UnresolvableGrantAlertsLoud(t *testing.T) {
	alerts := &fakeAlerts{}
	c := newTestChecker(&fakeReader{}, AuthModeCapability, alerts)
	Preflight(context.Background(), c, "vtx.identity.OPERATOR", nil)
	if len(alerts.calls) != 1 || alerts.calls[0] != "control-operator-grant-unresolved" {
		t.Fatalf("expected one control-operator-grant-unresolved alert, got %v", alerts.calls)
	}
}

func TestPreflight_InfraErrorAlertsDistinctlyFromMissingGrant(t *testing.T) {
	boom := errors.New("kv unavailable")
	reader := &fakeReader{errs: map[string]error{"cap.roles.identity.OPERATOR": boom}}
	alerts := &fakeAlerts{}
	c := newTestChecker(reader, AuthModeCapability, alerts)
	Preflight(context.Background(), c, "vtx.identity.OPERATOR", nil)
	if len(alerts.calls) != 1 || alerts.calls[0] != "control-preflight-probe-failed" {
		t.Fatalf("an infra read error must alert control-preflight-probe-failed (not control-operator-grant-unresolved), got %v", alerts.calls)
	}
}

func TestPreflight_ResolvableGrantIsSilent(t *testing.T) {
	alerts := &fakeAlerts{}
	reader := &fakeReader{docs: map[string][]byte{
		"cap.roles.identity.OPERATOR": docBytes(t, map[string]string{"operationType": "ctrl.weaver.disable", "scope": "any"}),
	}}
	c := newTestChecker(reader, AuthModeCapability, alerts)
	Preflight(context.Background(), c, "vtx.identity.OPERATOR", nil)
	if len(alerts.calls) != 0 {
		t.Fatalf("a resolvable grant must not alert, got %v", alerts.calls)
	}
}
