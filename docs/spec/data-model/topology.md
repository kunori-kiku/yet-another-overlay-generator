# Topology (Root Object)

```go
type Topology struct {
    Project       Project       `json:"project"`
    Domains       []Domain      `json:"domains"`
    Nodes         []Node        `json:"nodes"`
    Edges         []Edge        `json:"edges"`
    RoutePolicies []RoutePolicy `json:"route_policies,omitempty"`
}
```

See: [domain.md](domain.md), [node.md](node.md), [edge.md](edge.md),
[route-policy.md](route-policy.md).

## Project

```go
type Project struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Version     string `json:"version,omitempty"`
}
```
