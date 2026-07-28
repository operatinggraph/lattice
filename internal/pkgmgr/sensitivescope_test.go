package pkgmgr

import (
	"strings"
	"testing"
)

func TestValidateSensitiveClassScope_AspectTypeIsValid(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName: "ssn",
		Class:         "meta.ddl.aspectType",
		Sensitive:     true,
	}}}
	if err := def.validateSensitiveClassScope(); err != nil {
		t.Fatalf("expected a sensitive aspectType DDL to pass, got: %v", err)
	}
}

func TestValidateSensitiveClassScope_NonSensitiveAnyClassIsValid(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{
		{CanonicalName: "leaseapp", Class: "meta.ddl.vertexType"},
		{CanonicalName: "hasBooking", Class: "meta.ddl.linkType"},
		{CanonicalName: "leaseSigned", Class: "meta.ddl.eventType"},
		{CanonicalName: "nickname", Class: "meta.ddl.aspectType"},
		{CanonicalName: "defaultsToVertexType"}, // empty Class
	}}
	if err := def.validateSensitiveClassScope(); err != nil {
		t.Fatalf("expected non-sensitive DDLs of any class to pass, got: %v", err)
	}
}

func TestValidateSensitiveClassScope_SensitiveLinkTypeRejected(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName: "hasBooking",
		Class:         "meta.ddl.linkType",
		Sensitive:     true,
	}}}
	err := def.validateSensitiveClassScope()
	if err == nil {
		t.Fatal("expected error for a Sensitive linkType DDL, got nil")
	}
	if !strings.Contains(err.Error(), "hasBooking") || !strings.Contains(err.Error(), "meta.ddl.linkType") {
		t.Errorf("error should name the offending DDL and its Class; got %q", err)
	}
}

func TestValidateSensitiveClassScope_SensitiveEventTypeRejected(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName: "leaseSigned",
		Class:         "meta.ddl.eventType",
		Sensitive:     true,
	}}}
	if err := def.validateSensitiveClassScope(); err == nil {
		t.Fatal("expected error for a Sensitive eventType DDL, got nil")
	}
}

func TestValidateSensitiveClassScope_SensitiveEmptyClassDefaultsToVertexTypeRejected(t *testing.T) {
	// An omitted Class defaults to vertexType (buildInstallBatch's own
	// default) — never aspectType — so Sensitive:true with no Class must
	// reject exactly like an explicit vertexType would.
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName: "widget",
		Sensitive:     true,
	}}}
	err := def.validateSensitiveClassScope()
	if err == nil {
		t.Fatal("expected error for a Sensitive DDL with an omitted (vertexType-defaulted) Class, got nil")
	}
	if !strings.Contains(err.Error(), "meta.ddl.vertexType") {
		t.Errorf("error should name the defaulted Class; got %q", err)
	}
}
