# syntax=docker/dockerfile:1

# ---- deps: 依存関係のインストール ----
FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

# ---- build: Next.js (standalone) のビルド ----
FROM node:22-alpine AS build
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# ビルドに Directus 等の環境変数は不要（設定は実行時に環境変数から読む）
RUN npm run build

# ---- runner: 実行イメージ（非 root）----
FROM node:22-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000 \
    HOSTNAME=0.0.0.0

COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public
# scripts/seed-admin.mjs が使う（standalone 出力は bcryptjs をバンドルして node_modules に残さないため明示コピー）
COPY --from=deps --chown=node:node /app/node_modules/bcryptjs ./node_modules/bcryptjs
COPY --chown=node:node scripts ./scripts
COPY --chown=node:node docker-entrypoint.sh ./docker-entrypoint.sh

USER node
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:3000/api/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["./docker-entrypoint.sh"]
