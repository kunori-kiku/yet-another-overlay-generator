# Validation

## Pass 1: Schema Validation (`validator.ValidateSchema`)

Structural checks on the raw topology JSON:
- Required fields present (project ID/name, domain CIDR, node role, etc.)
- CIDR format validity
- Enum value validity (roles, routing modes, transport protocols)
- Port range validity (0–65535)
- No self-loops on edges

## Pass 2: Semantic Validation (`validator.ValidateSemantic`)

Cross-reference and logical checks:
- Node domain_id references exist
- Edge from/to node references exist
- Overlay IPs within domain CIDRs
- No duplicate IDs (domains, nodes, edges)
- No IP address collisions
- Listen port conflicts (same hostname)
- Isolated node detection (warning)
- NAT reachability warnings (double-NAT, no public endpoint)
- Client edge constraints (exactly one outbound, must target router/relay/gateway, must have endpoint_host)
