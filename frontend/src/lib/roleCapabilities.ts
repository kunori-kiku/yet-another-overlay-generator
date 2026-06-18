// Role -> capabilities derivation, shared by NodeForm and RightPanel.
// Kept separate from component files: react-refresh/only-export-components requires component
// files to export only components, so shared pure functions must live in a non-component module.
import type { NodeCapabilities, Node } from '../types/topology';

export type NodeRole = Node['role'];

// Derive capabilities from the role, consistent with the backend's InferCapabilitiesFromRole in roles.go:
// router/relay/gateway can forward; relay additionally accepts inbound and relays; client is all false.
// This keeps the caps the frontend sends from contradicting the role inference (D69/D54). Preserve the operator's explicitly set has_public_ip.
export function deriveCapabilitiesFromRole(role: NodeRole, hasPublicIP: boolean): NodeCapabilities {
  switch (role) {
    case 'router':
      return {
        can_forward: true,
        // router/gateway accepts inbound connections when it has a public IP (consistent with backend D49)
        can_accept_inbound: hasPublicIP,
        can_relay: false,
        has_public_ip: hasPublicIP,
      };
    case 'gateway':
      return {
        can_forward: true,
        can_accept_inbound: hasPublicIP,
        can_relay: false,
        has_public_ip: hasPublicIP,
      };
    case 'relay':
      return {
        can_forward: true,
        can_accept_inbound: true,
        can_relay: true,
        has_public_ip: hasPublicIP,
      };
    case 'client':
      return {
        can_forward: false,
        can_accept_inbound: false,
        can_relay: false,
        has_public_ip: false,
      };
    default: // 'peer'
      return {
        can_forward: false,
        can_accept_inbound: hasPublicIP,
        can_relay: false,
        has_public_ip: hasPublicIP,
      };
  }
}
