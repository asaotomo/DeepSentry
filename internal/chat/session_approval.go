package chat

// Grants live in the chat service, not the short-lived Agent child. Owner
// includes channel, conversation and sender; every session change clears it.
// Restart deliberately revokes grants instead of silently restoring authority.
func (s *Service) rememberApprovalLocked(own, scope, decision string) {
	if decision != "allow_session" && decision != "allow_all_session" {
		return
	}
	if s.approvals == nil {
		s.approvals = make(map[string]*sessionGrant)
	}
	g := s.approvals[own]
	if g == nil {
		g = &sessionGrant{scopes: make(map[string]bool)}
		s.approvals[own] = g
	}
	if decision == "allow_all_session" {
		g.all = true
	} else if scope != "" {
		g.scopes[scope] = true
	}
}
func (s *Service) hasApprovalLocked(own, scope string) bool {
	g := s.approvals[own]
	return g != nil && (g.all || scope != "" && g.scopes[scope])
}
