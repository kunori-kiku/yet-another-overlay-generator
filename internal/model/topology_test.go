package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// LoadTopologyFromFile 从文件加载拓扑（测试辅助函数）
func LoadTopologyFromFile(path string) (*Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var topo Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		return nil, err
	}
	return &topo, nil
}

func examplesDir() string {
	// 从 internal/model/ 回到项目根目录下的 examples/
	return filepath.Join("..", "..", "examples")
}

func TestLoadSimpleMesh(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "simple-mesh", "topology.json"))
	if err != nil {
		t.Fatalf("加载 simple-mesh 拓扑失败: %v", err)
	}

	// 检查 Project
	if topo.Project.ID != "simple-mesh-001" {
		t.Errorf("Project ID 期望 simple-mesh-001, 得到 %s", topo.Project.ID)
	}
	if topo.Project.Name != "Simple Mesh" {
		t.Errorf("Project Name 期望 Simple Mesh, 得到 %s", topo.Project.Name)
	}

	// 检查 Domain
	if len(topo.Domains) != 1 {
		t.Fatalf("期望 1 个 Domain, 得到 %d", len(topo.Domains))
	}
	if topo.Domains[0].CIDR != "10.10.0.0/24" {
		t.Errorf("Domain CIDR 期望 10.10.0.0/24, 得到 %s", topo.Domains[0].CIDR)
	}
	if topo.Domains[0].RoutingMode != "babel" {
		t.Errorf("Domain RoutingMode 期望 babel, 得到 %s", topo.Domains[0].RoutingMode)
	}

	// 检查节点
	if len(topo.Nodes) != 3 {
		t.Fatalf("期望 3 个节点, 得到 %d", len(topo.Nodes))
	}
	for _, node := range topo.Nodes {
		if node.Role != "router" {
			t.Errorf("节点 %s 的角色期望 router, 得到 %s", node.Name, node.Role)
		}
		if !node.Capabilities.HasPublicIP {
			t.Errorf("节点 %s 期望有公网 IP", node.Name)
		}
	}

	// 检查边（全互联 = 6 条单向边）
	if len(topo.Edges) != 6 {
		t.Fatalf("期望 6 条边, 得到 %d", len(topo.Edges))
	}
}

func TestLoadNatHub(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "nat-hub", "topology.json"))
	if err != nil {
		t.Fatalf("加载 nat-hub 拓扑失败: %v", err)
	}

	if topo.Project.ID != "nat-hub-001" {
		t.Errorf("Project ID 期望 nat-hub-001, 得到 %s", topo.Project.ID)
	}

	// 检查节点角色
	if len(topo.Nodes) != 3 {
		t.Fatalf("期望 3 个节点, 得到 %d", len(topo.Nodes))
	}

	hub := topo.Nodes[0]
	if hub.Role != "router" {
		t.Errorf("Hub 角色期望 router, 得到 %s", hub.Role)
	}
	if !hub.Capabilities.CanRelay {
		t.Errorf("Hub 期望 can_relay=true")
	}
	if !hub.Capabilities.HasPublicIP {
		t.Errorf("Hub 期望 has_public_ip=true")
	}

	// NAT 客户端
	for _, client := range topo.Nodes[1:] {
		if client.Role != "peer" {
			t.Errorf("客户端 %s 角色期望 peer, 得到 %s", client.Name, client.Role)
		}
		if client.Capabilities.HasPublicIP {
			t.Errorf("客户端 %s 不应有公网 IP", client.Name)
		}
		if client.Capabilities.CanAcceptInbound {
			t.Errorf("客户端 %s 不应接受入站", client.Name)
		}
	}

	// 只有 2 条边（客户端主动连 hub）
	if len(topo.Edges) != 2 {
		t.Fatalf("期望 2 条边, 得到 %d", len(topo.Edges))
	}
}

func TestLoadRelayTopology(t *testing.T) {
	topo, err := LoadTopologyFromFile(filepath.Join(examplesDir(), "relay-topology", "topology.json"))
	if err != nil {
		t.Fatalf("加载 relay-topology 拓扑失败: %v", err)
	}

	if topo.Project.ID != "relay-topo-001" {
		t.Errorf("Project ID 期望 relay-topo-001, 得到 %s", topo.Project.ID)
	}

	// 检查中继节点
	relay := topo.Nodes[0]
	if relay.Role != "relay" {
		t.Errorf("中继节点角色期望 relay, 得到 %s", relay.Role)
	}
	if !relay.Capabilities.CanRelay {
		t.Errorf("中继节点期望 can_relay=true")
	}

	// 检查手动 IP 分配
	peer2 := topo.Nodes[2]
	if peer2.OverlayIP != "10.30.0.100" {
		t.Errorf("peer-2 手动 IP 期望 10.30.0.100, 得到 %s", peer2.OverlayIP)
	}

	// peer-1 无手动 IP（应由自动分配）
	peer1 := topo.Nodes[1]
	if peer1.OverlayIP != "" {
		t.Errorf("peer-1 不应有手动 IP, 得到 %s", peer1.OverlayIP)
	}

	// 2 条边
	if len(topo.Edges) != 2 {
		t.Fatalf("期望 2 条边, 得到 %d", len(topo.Edges))
	}
}

func TestTopologyDefaultValues(t *testing.T) {
	// 测试 JSON 反序列化时可选字段的默认零值
	jsonData := `{
		"project": {"id": "test", "name": "Test"},
		"domains": [],
		"nodes": [],
		"edges": []
	}`

	var topo Topology
	if err := json.Unmarshal([]byte(jsonData), &topo); err != nil {
		t.Fatalf("反序列化最小拓扑失败: %v", err)
	}

	if topo.Project.Description != "" {
		t.Errorf("Description 默认值期望空字符串, 得到 %s", topo.Project.Description)
	}
	if topo.Project.Version != "" {
		t.Errorf("Version 默认值期望空字符串, 得到 %s", topo.Project.Version)
	}
	if topo.RoutePolicies != nil {
		t.Errorf("RoutePolicies 默认值期望 nil, 得到 %v", topo.RoutePolicies)
	}
}

func TestNodeSerialization(t *testing.T) {
	node := Node{
		ID:       "test-node",
		Name:     "test",
		Role:     "peer",
		DomainID: "domain-1",
		Capabilities: NodeCapabilities{
			CanAcceptInbound: false,
			CanForward:       false,
			CanRelay:         false,
			HasPublicIP:      false,
		},
	}

	data, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("序列化 Node 失败: %v", err)
	}

	var decoded Node
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("反序列化 Node 失败: %v", err)
	}

	if decoded.ID != node.ID {
		t.Errorf("ID 不匹配: 期望 %s, 得到 %s", node.ID, decoded.ID)
	}
	if decoded.OverlayIP != "" {
		t.Errorf("OverlayIP 应为空, 得到 %s", decoded.OverlayIP)
	}
	if decoded.ListenPort != 0 {
		t.Errorf("ListenPort 应为 0, 得到 %d", decoded.ListenPort)
	}
}
