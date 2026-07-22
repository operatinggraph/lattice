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
// (opList/opDisable/opEnable/opRevoke/opResetConfidence). A per-cmd wiring
// test asserts this table stays in lockstep with the service's dispatch tables
// (design R5). resetConfidence carries its own verb rather than reusing
// revoke's: it is a strictly narrower deletion (advisory confidence windows
// only), and folding it under an existing verb would grant the wider one to
// every actor that needs the narrower.
var WeaverOps = map[string]OpMeta{
	"list":            {Verb: "read", Read: true},
	"disable":         {Verb: "disable", Read: false},
	"enable":          {Verb: "enable", Read: false},
	"revoke":          {Verb: "revoke", Read: false},
	"resetConfidence": {Verb: "resetConfidence", Read: false},
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
// (health, validate, rebuild, pause, resume, delete, register, deregister,
// hydrate, sessionkey, syncgap). register/deregister/hydrate (Personal Lens
// interest-set registration + initial sync, Fire PL.2 /
// per-identity-nats-subscribe-acl Fire 2) and sessionkey (transient Vault
// session key, edge-lattice-full-design.md §3.6, EDGE.4) postdate the FR30
// design doc's §2(c) table; classified mutate here under the same
// each-mutation-is-its-own-verb principle the design applies to every other
// component mutation. syncgap (edge-syncgap-control-rpc-design.md) is its own
// read verb — granting the generic ctrl.refractor.read would also open
// health/validate on every lens (a topology leak); it is honestly Read: true
// (it reveals one derived bit of stream state, mutates nothing).
var RefractorOps = map[string]OpMeta{
	"health":     {Verb: "read", Read: true},
	"validate":   {Verb: "read", Read: true},
	"rebuild":    {Verb: "rebuild", Read: false},
	"pause":      {Verb: "pause", Read: false},
	"resume":     {Verb: "resume", Read: false},
	"delete":     {Verb: "delete", Read: false},
	"register":   {Verb: "register", Read: false},
	"deregister": {Verb: "deregister", Read: false},
	"hydrate":    {Verb: "hydrate", Read: false},
	"sessionkey": {Verb: "sessionkey", Read: false},
	"syncgap":    {Verb: "syncgap", Read: true},
}
