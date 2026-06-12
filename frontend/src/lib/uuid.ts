// crypto.randomUUID() 只在安全上下文（HTTPS 或 localhost）暴露 —— 通过
// http://<局域网 IP> 访问面板时它是 undefined，会抛出
// "crypto.randomUUID is not a function"。底层的 crypto.getRandomValues()
// 没有这个限制，所以非安全上下文下用它手写 UUIDv4 兜底（RFC 4122：
// 第 6 字节高 4 位置 version=4，第 8 字节高 2 位置 variant=10）。
// 所有需要客户端随机 ID 的地方一律用这个函数，不要直接调 crypto.randomUUID。
export function uuid(): string {
  if (typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
