// 角色 -> capabilities 推导，供 NodeForm 与 RightPanel 共用。
// 独立于组件文件存放：react-refresh/only-export-components 要求组件文件只导出组件，
// 共享的纯函数必须放在非组件模块中。
import type { NodeCapabilities, Node } from '../types/topology';

export type NodeRole = Node['role'];

// 从角色推导 capabilities，与后端 roles.go 的 InferCapabilitiesFromRole 保持一致：
// router/relay/gateway 可转发；relay 额外接受入站并中继；client 全部为 false。
// 这样前端发送的 caps 不会与角色推断相矛盾（D69/D54）。保留操作员显式设置的 has_public_ip。
export function deriveCapabilitiesFromRole(role: NodeRole, hasPublicIP: boolean): NodeCapabilities {
  switch (role) {
    case 'router':
      return {
        can_forward: true,
        // router/gateway 在具备公网 IP 时接受入站连接（与后端 D49 一致）
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
