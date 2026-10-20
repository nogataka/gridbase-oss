/** @type {import('next').NextConfig} */
const nextConfig = {
  // Docker では .next/standalone を実行する（node server.js）
  output: 'standalone',
  reactStrictMode: true,
  poweredByHeader: false,
  // 画像最適化 API は使わない（<img> のみ）
  images: { unoptimized: true },
  // セキュリティヘッダー(CSP/HSTS 等)は src/middleware.ts で付与する。
  // next.config.js の headers() はビルド時に固定されるため、
  // 実行時の環境変数（NEXTAUTH_URL が https か）で出し分けられない。
};

module.exports = nextConfig;
