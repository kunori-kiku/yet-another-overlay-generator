# RoutePolicy

```go
type RoutePolicy struct {
    ID              string `json:"id"`
    DomainID        string `json:"domain_id"`
    DestinationCIDR string `json:"destination_cidr"`
    NextHopNodeID   string `json:"next_hop_node_id,omitempty"`
    Metric          int    `json:"metric,omitempty"`
    Notes           string `json:"notes,omitempty"`
    SourceSelector  string `json:"source_selector,omitempty"`
    Action          string `json:"action,omitempty"`
    ApplyToNodeID   string `json:"apply_to_node_id,omitempty"`
}
```
