package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// shippedExampleTopologies 列出仓库中随产品发布的示例拓扑。
// 路径相对于本包目录（internal/compiler）。
var shippedExampleTopologies = []string{
	"../../examples/simple-mesh/topology.json",
	"../../examples/nat-hub/topology.json",
	"../../examples/relay-topology/topology.json",
}

// loadExampleTopology 读取并反序列化一个示例拓扑文件。
func loadExampleTopology(t *testing.T, path string) *model.Topology {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("无法读取示例拓扑 %s: %v", path, err)
	}
	var topo model.Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		t.Fatalf("无法解析示例拓扑 %s: %v", path, err)
	}
	return &topo
}

// exampleKeys 为拓扑中的每个节点生成一份测试用密钥对，
// 使编译产物完整填充（与 testKeys 风格一致，但按节点 ID 动态生成）。
func exampleKeys(topo *model.Topology) map[string]KeyPair {
	keys := make(map[string]KeyPair, len(topo.Nodes))
	for _, node := range topo.Nodes {
		keys[node.ID] = KeyPair{
			PrivateKey: "privkey-" + node.ID + "-fake",
			PublicKey:  "pubkey-" + node.ID + "-fake",
		}
	}
	return keys
}

// TestExampleTopologiesDialCorrectPorts 是“示例永远可部署”的守门测试。
//
// 它对每个随产品发布的示例拓扑运行完整编译流水线，然后验证端口归属
// 的核心不变量：对于结果 PeerMap 中每个带有非空 Endpoint 的 PeerInfo，
// 其 Endpoint 中拨出的端口必须等于对端节点为这条链路分配的接口监听端口
// （即对端 PeerMap 中指回本节点的那个 PeerInfo 的 ListenPort）。
//
// 一旦有人重新在示例 edge 上写死 endpoint_port（曾经的头号缺陷：把节点
// 的 public_endpoints[0].port 当成每条链路的拨号端口），拨出端口就会偏离
// 对端实际监听的端口，本测试随即失败。
func TestExampleTopologiesDialCorrectPorts(t *testing.T) {
	for _, relPath := range shippedExampleTopologies {
		relPath := relPath
		t.Run(filepath.Base(filepath.Dir(relPath)), func(t *testing.T) {
			topo := loadExampleTopology(t, relPath)
			keys := exampleKeys(topo)

			c := NewCompiler()
			result, err := c.Compile(topo, keys)
			if err != nil {
				t.Fatalf("示例 %s 编译失败: %v", relPath, err)
			}

			// 对每个节点的每个带 endpoint 的 peer，验证拨出端口
			// 等于对端为本节点分配的接口监听端口。
			for nodeID, peers := range result.PeerMap {
				for _, p := range peers {
					if p.Endpoint == "" {
						continue // 无 endpoint 的被动 peer 跳过
					}

					dialedPort := extractPortFromEndpoint(p.Endpoint)

					// 在对端节点的 peer 列表中找到指回本节点的条目。
					remotePeers := result.PeerMap[p.NodeID]
					found := false
					for _, rp := range remotePeers {
						if rp.NodeID != nodeID {
							continue
						}
						found = true
						if dialedPort != rp.ListenPort {
							t.Errorf("示例 %s: %s→%s 拨出端口=%d (endpoint %q)，"+
								"但 %s 为 %s 分配的接口 ListenPort=%d",
								relPath, nodeID, p.NodeID, dialedPort, p.Endpoint,
								p.NodeID, nodeID, rp.ListenPort)
						}
						break
					}
					if !found {
						t.Errorf("示例 %s: %s 有指向 %s 的 peer (带 endpoint %q)，"+
							"但 %s 没有指回 %s 的反向 peer",
							relPath, nodeID, p.NodeID, p.Endpoint, p.NodeID, nodeID)
					}
				}
			}
		})
	}
}
