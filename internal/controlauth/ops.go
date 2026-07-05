package controlauth

// OpMeta describes one control-plane op token: the verb its
// `ctrl.<component>.<verb>` capability grant carries, and whether it is a
// read (topology-revealing) or mutate op. Read ops are gated too (R3,
// control-plane-capability-authz-design.md) — an ungranted actor may not even
// enumerate targets.
type OpMeta struct {
	Verb string
	Read bool
}

// WeaverOps mirrors internal/weaver/control/service.go's op constants
// (opList/opDisable/opEnable/opRevoke). A per-cmd wiring test asserts this
// table stays in lockstep with the service's dispatch tables (design R5).
var WeaverOps = map[string]OpMeta{
	"list":    {Verb: "read", Read: true},
	"disable": {Verb: "disable", Read: false},
	"enable":  {Verb: "enable", Read: false},
	"revoke":  {Verb: "revoke", Read: false},
}

// LoomOps mirrors internal/loom/control/service.go's exactOps/nameOps
// (list, consumers, inspect, pause, resume).
var LoomOps = map[string]OpMeta{
	"list":      {Verb: "read", Read: true},
	"consumers": {Verb: "read", Read: true},
	"inspect":   {Verb: "read", Read: true},
	"pause":     {Verb: "pause", Read: false},
	"resume":    {Verb: "resume", Read: false},
}

// RefractorOps mirrors internal/refractor/control/service.go's supportedOps
// (health, validate, rebuild, pause, resume, delete, register, deregister).
// register/deregister (Personal Lens interest-set registration, Fire PL.2)
// postdate the FR30 design doc's §2(c) table; classified mutate here under
// the same each-mutation-is-its-own-verb principle the design applies to
// every other component mutation.
var RefractorOps = map[string]OpMeta{
	"health":     {Verb: "read", Read: true},
	"validate":   {Verb: "read", Read: true},
	"rebuild":    {Verb: "rebuild", Read: false},
	"pause":      {Verb: "pause", Read: false},
	"resume":     {Verb: "resume", Read: false},
	"delete":     {Verb: "delete", Read: false},
	"register":   {Verb: "register", Read: false},
	"deregister": {Verb: "deregister", Read: false},
}
