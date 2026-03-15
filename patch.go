var checksumLines []string
if wgConf, ok := result.WireGuardConfigs[node.ID]; ok {
checksumLines = append(checksumLines, fmt.Sprintf("%x  wireguard/wg0.conf", sha256.Sum256([]byte(wgConf))))
}
if babelConf, ok := result.BabelConfigs[node.ID]; ok {
checksumLines = append(checksumLines, fmt.Sprintf("%x  babel/babeld.conf", sha256.Sum256([]byte(babelConf))))
}
if sysctlConf, ok := result.SysctlConfigs[node.ID]; ok {
checksumLines = append(checksumLines, fmt.Sprintf("%x  sysctl/99-overlay.conf", sha256.Sum256([]byte(sysctlConf))))
}

// 写入 checksums.sha256
checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
if err := os.WriteFile(checksumsPath, []byte(strings.Join(checksumLines, "\n")), 0644); err != nil {
return nil, fmt.Errorf("写入 checksums.sha256 失败: %w", err)
}
