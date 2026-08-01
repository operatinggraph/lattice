package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildOpGroups(t *testing.T) {
	store := map[string][]byte{
		// A multi-command DDL meta (a "service").
		"vtx.meta.rbac9999999999999999":                   []byte(`{"class":"meta.ddl.vertexType","data":{"note":"x"}}`),
		"vtx.meta.rbac9999999999999999.permittedCommands": []byte(`{"data":{"commands":["CreateRole","UpdateRole"]}}`),
		"vtx.meta.rbac9999999999999999.canonicalName":     []byte(`{"data":{"value":"rbac"}}`),
		"vtx.meta.rbac9999999999999999.description":       []byte(`{"data":{"value":"role-based access control"}}`),
		"vtx.meta.rbac9999999999999999.inputSchema":       []byte(`{"data":{"schema":"{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"}}}"}}`),
		// A single-command meta.
		"vtx.meta.inst9999999999999999":                   []byte(`{"class":"meta.ddl.vertexType","data":{}}`),
		"vtx.meta.inst9999999999999999.permittedCommands": []byte(`{"data":{"commands":["InstallPackage"]}}`),
		"vtx.meta.inst9999999999999999.canonicalName":     []byte(`{"data":{"value":"InstallPackage"}}`),
		// A lens meta — no permittedCommands → not a submittable op, must be dropped.
		"vtx.meta.Lens9999999999999999": []byte(`{"class":"meta.lens","data":{}}`),
		// A commands-bearing meta with NO canonicalName → falls back to the id.
		"vtx.meta.noname99999999999999":                   []byte(`{"class":"meta.ddl.vertexType","data":{}}`),
		"vtx.meta.noname99999999999999.permittedCommands": []byte(`{"data":{"commands":["DoThing"]}}`),
		// A meta whose inputSchema is malformed → InputSchema must be dropped.
		"vtx.meta.bad99999999999999999":                   []byte(`{"class":"meta.ddl.vertexType","data":{}}`),
		"vtx.meta.bad99999999999999999.permittedCommands": []byte(`{"data":{"commands":["BadOp"]}}`),
		"vtx.meta.bad99999999999999999.canonicalName":     []byte(`{"data":{"value":"badschema"}}`),
		"vtx.meta.bad99999999999999999.inputSchema":       []byte(`{"data":{"schema":"{not json"}}`),
		// An aspectType DDL that re-lists CreateRole (owned by the rbac vertexType)
		// only as a class-inference target → its single command is a duplicate, so
		// the whole group must be dropped.
		"vtx.meta.dup99999999999999999":                   []byte(`{"class":"meta.ddl.aspectType","data":{}}`),
		"vtx.meta.dup99999999999999999.permittedCommands": []byte(`{"data":{"commands":["CreateRole"]}}`),
		"vtx.meta.dup99999999999999999.canonicalName":     []byte(`{"data":{"value":"dupAspect"}}`),
		// An aspectType DDL whose op no vertexType owns → it must survive.
		"vtx.meta.uniq9999999999999999":                   []byte(`{"class":"meta.ddl.aspectType","data":{}}`),
		"vtx.meta.uniq9999999999999999.permittedCommands": []byte(`{"data":{"commands":["UniqueOp"]}}`),
		"vtx.meta.uniq9999999999999999.canonicalName":     []byte(`{"data":{"value":"uniqueAspect"}}`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }

	metaKeys := []string{
		"vtx.meta.rbac9999999999999999",
		"vtx.meta.inst9999999999999999",
		"vtx.meta.Lens9999999999999999",
		"vtx.meta.noname99999999999999",
		"vtx.meta.bad99999999999999999",
		"vtx.meta.dup99999999999999999",
		"vtx.meta.uniq9999999999999999",
		"vtx.identity.notameta0000000", // not a meta → filtered out
	}

	groups := buildOpGroups(metaKeys, get)

	byName := map[string]opGroup{}
	for _, g := range groups {
		byName[g.Name] = g
	}

	if _, ok := byName["lens"]; ok {
		t.Error("a meta.lens with no permittedCommands must not appear as an op group")
	}
	if _, ok := byName["dupAspect"]; ok {
		t.Error("an aspectType group whose only op is owned by a vertexType must be dropped")
	}
	if uniq, ok := byName["uniqueAspect"]; !ok || len(uniq.Commands) != 1 || uniq.Commands[0] != "UniqueOp" {
		t.Errorf("aspectType op with no vertexType owner must survive; got %+v", uniq)
	}
	if len(groups) != 5 {
		t.Fatalf("got %d groups, want 5 (InstallPackage, rbac, noname, badschema, uniqueAspect); %+v", len(groups), groups)
	}

	rbac, ok := byName["rbac"]
	if !ok {
		t.Fatalf("rbac group missing")
	}
	if len(rbac.Commands) != 2 || rbac.Commands[0] != "CreateRole" {
		t.Errorf("rbac commands = %v", rbac.Commands)
	}
	if rbac.Class != "meta.ddl.vertexType" {
		t.Errorf("rbac class = %q", rbac.Class)
	}
	if rbac.Description != "role-based access control" {
		t.Errorf("rbac description = %q", rbac.Description)
	}
	if rbac.InputSchema == nil || !json.Valid(rbac.InputSchema) || !strings.Contains(string(rbac.InputSchema), "properties") {
		t.Errorf("rbac inputSchema not forwarded as valid JSON: %s", rbac.InputSchema)
	}

	// canonicalName fallback: a meta with none takes its id suffix as the name.
	if _, ok := byName["noname99999999999999"]; !ok {
		t.Errorf("expected id-fallback group name; groups: %+v", groups)
	}

	// a malformed inputSchema string is dropped (the op still lists, just no form).
	if bad := byName["badschema"]; bad.InputSchema != nil {
		t.Errorf("malformed inputSchema should be dropped, got %s", bad.InputSchema)
	}

	// groups are sorted by name (ASCII: capitals before lowercase).
	if groups[0].Name != "InstallPackage" {
		t.Errorf("groups not sorted by name; first = %q", groups[0].Name)
	}
}
