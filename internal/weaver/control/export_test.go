package control

import (
	"fmt"
	"strings"
)

// TargetIDFromSubject exposes the unexported targetIDFromSubject for the
// control_test package. The disable/enable/revoke endpoints register on the
// wildcard subject lattice.ctrl.weaver.*.<op>, so NATS subject routing can
// only ever deliver a conforming 5-token subject to dispatchEndpoint — the
// parser's deviation branches are an unreachable-via-NATS defensive boundary.
// Exposing the helper lets those branches be table-tested directly, guarding
// against a future direct caller or a refactor that loosens the wildcard.
var TargetIDFromSubject = targetIDFromSubject

// RegisteredExactOps and RegisteredTargetOps expose the op tokens the
// registration loops range over, split by subject shape. They are the
// declared set; ServedEndpoints is the published one, and a guard that checks
// only the declared set covers only the registration paths that use it.
func RegisteredExactOps() []string { return append([]string(nil), exactOps...) }

func RegisteredTargetOps() []string { return append([]string(nil), targetOps...) }

// ServedEndpoint is one endpoint the running service has published: the op
// token it dispatches under, and the subject NATS routes to it.
type ServedEndpoint struct {
	Op      string
	Subject string
}

// ServedEndpoints reports what the RUNNING service actually published, read
// from the live micro registration rather than from the vars the registration
// loops range over. That is the difference between a guard that covers this
// service's registration loops and one that covers every path an endpoint can
// reach NATS by: a direct AddEndpoint call outside both loops publishes a real,
// routable, authorization-checked op that no var mentions.
//
// The op token is the LAST subject segment, for both shapes this service
// registers — "lattice.ctrl.weaver.<op>" and "lattice.ctrl.weaver.*.<op>". A
// subject of any other shape is an error rather than a skip: silently ignoring
// an endpoint whose shape this parser does not know would reopen the same hole
// one subject family over.
func (s *Service) ServedEndpoints() ([]ServedEndpoint, error) {
	s.mu.Lock()
	svc := s.microSvc
	s.mu.Unlock()
	if svc == nil {
		return nil, fmt.Errorf("control: no NATS listener started; nothing is published yet")
	}
	var out []ServedEndpoint
	for _, ep := range svc.Info().Endpoints {
		parts := strings.Split(ep.Subject, ".")
		if len(parts) < 4 || len(parts) > 5 || parts[0] != "lattice" || parts[1] != "ctrl" || parts[2] != "weaver" {
			return nil, fmt.Errorf("endpoint %q publishes %q, which is neither subject shape this service registers",
				ep.Name, ep.Subject)
		}
		if len(parts) == 5 && parts[3] != "*" {
			return nil, fmt.Errorf("endpoint %q publishes %q, whose target segment is not the registered wildcard",
				ep.Name, ep.Subject)
		}
		out = append(out, ServedEndpoint{Op: parts[len(parts)-1], Subject: ep.Subject})
	}
	return out, nil
}
