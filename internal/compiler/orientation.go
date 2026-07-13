package compiler

// oriented returns this allocation's (from, to) resource sextuple as seen from a given
// direction. A pairAllocation stores its ports / transit IPs / link-locals against a fixed
// canonical from/to (fromNodeID/toNodeID); a consumer walking an edge or peer in the SAME
// direction as the alloc (isForward) reads them straight through, while the opposite
// direction (isForward==false) mirrors every pair. This is the single forward/reverse swap
// shared by the pin write-back (compiler.go) and the forward / reverse / client-router
// PeerInfo builders (peers_build.go); the "reverse side" of a link is just oriented(!isForward).
func (a *pairAllocation) oriented(isForward bool) (fromPort, toPort int, fromTransitIP, toTransitIP, fromLinkLocal, toLinkLocal string) {
	if isForward {
		return a.fromPort, a.toPort, a.localTransit, a.remoteTransit, a.localLL, a.remoteLL
	}
	return a.toPort, a.fromPort, a.remoteTransit, a.localTransit, a.remoteLL, a.localLL
}
