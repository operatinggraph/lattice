package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// opGroup is one service/domain in the Submit Op catalog: a meta-vertex (DDL)
// that governs one or more submittable operations. Commands lists the operation
// types the meta permits; InputSchema is the meta's JSON Schema for op payloads
// — a union across the meta's commands, where each property's description notes
// which command it applies to. Returned by GET /api/ops.
type opGroup struct {
	Name        string          `json:"name"`
	MetaKey     string          `json:"metaKey"`
	Class       string          `json:"class,omitempty"`
	Description string          `json:"description,omitempty"`
	Commands    []string        `json:"commands"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// kvGetter reads a Core KV envelope's raw bytes for a key, reporting false when
// the key is absent or unreadable. It is the seam buildOpGroups and the Health
// lens resolver are tested over.
type kvGetter func(key string) ([]byte, bool)

// buildOpGroups turns the meta-vertex roots in metaKeys into the Submit Op
// catalog. A meta with no permittedCommands is not a submittable operation (a
// lens, a linkType, …) and is dropped. An operation defined on a
// meta.ddl.vertexType is "owned" there; an aspectType / eventType DDL that
// re-lists the same op in permittedCommands does so only as a class-inference
// target, so that duplicate is dropped (an aspectType group survives only for
// ops no vertexType owns). Groups are sorted by canonicalName for stable UI
// ordering.
func buildOpGroups(metaKeys []string, get kvGetter) []opGroup {
	type metaInfo struct {
		mk     string
		class  string
		cmds   []string
		name   string
		desc   string
		schema json.RawMessage
	}

	infos := make([]metaInfo, 0, len(metaKeys))
	owned := make(map[string]bool) // ops owned by a vertexType meta
	for _, mk := range metaKeys {
		if classifyKey(mk) != classMeta {
			continue
		}
		cmds := dataStrings(metaData(get, mk+".permittedCommands"), "commands")
		if len(cmds) == 0 {
			continue
		}
		mi := metaInfo{mk: mk, cmds: cmds, class: envelopeClass(get, mk)}
		mi.name = dataString(metaData(get, mk+".canonicalName"), "value", "name", "canonicalName")
		if mi.name == "" {
			mi.name = strings.TrimPrefix(mk, "vtx.meta.")
		}
		mi.desc = dataString(metaData(get, mk+".description"), "value", "text", "description")
		if schema := dataString(metaData(get, mk+".inputSchema"), "schema"); schema != "" && json.Valid([]byte(schema)) {
			mi.schema = json.RawMessage(schema)
		}
		infos = append(infos, mi)
		if mi.class == "meta.ddl.vertexType" {
			for _, c := range cmds {
				owned[c] = true
			}
		}
	}

	groups := make([]opGroup, 0, len(infos))
	for _, mi := range infos {
		cmds := mi.cmds
		if mi.class != "meta.ddl.vertexType" {
			rem := make([]string, 0, len(cmds))
			for _, c := range cmds {
				if !owned[c] {
					rem = append(rem, c)
				}
			}
			cmds = rem
		}
		if len(cmds) == 0 {
			continue
		}
		groups = append(groups, opGroup{
			Name:        mi.name,
			MetaKey:     mi.mk,
			Class:       mi.class,
			Description: mi.desc,
			Commands:    cmds,
			InputSchema: mi.schema,
		})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return groups
}

// metaData decodes the .data object of a Core KV envelope, returning nil when
// the key is absent or the value is not a JSON object carrying a data field.
func metaData(get kvGetter, key string) map[string]any {
	raw, ok := get(key)
	if !ok {
		return nil
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	return env.Data
}

// dataString returns the first non-empty string value among keys in d. The
// self-description aspects in use spell the payload "value" but older fixtures
// use "text"/"description"/etc., so callers pass the candidates in priority order.
func dataString(d map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := d[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// dataStrings returns d[key] as a []string, dropping any non-string elements.
func dataStrings(d map[string]any, key string) []string {
	arr, ok := d[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// envelopeClass returns the top-level "class" field of the Core KV envelope at
// key (e.g. "meta.ddl.vertexType"), or "" when absent.
func envelopeClass(get kvGetter, key string) string {
	raw, ok := get(key)
	if !ok {
		return ""
	}
	var env struct {
		Class string `json:"class"`
	}
	_ = json.Unmarshal(raw, &env)
	return env.Class
}

// handleOps implements GET /api/ops: the Submit Op catalog. It lists every Core
// KV meta-vertex, keeps the ones that govern submittable operations, and returns
// them grouped by canonicalName with their commands and input schema.
func (s *server) handleOps(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv: "+err.Error())
		return
	}
	metaKeys := make([]string, 0)
	for _, k := range keys {
		if classifyKey(k) == classMeta {
			metaKeys = append(metaKeys, k)
		}
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	groups := buildOpGroups(metaKeys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "count": len(groups)})
}
