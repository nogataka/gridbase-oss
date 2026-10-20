# syntax=docker/dockerfile:1
#
# directus-init の実行イメージ。bcrypt の比較（保存済みの管理者パスワードが既定値でないかの検査）に
# bcryptjs が必要なため、node:22-alpine に依存を入れて作る。docker compose up --build で自動的にビルドされる。
FROM node:22-alpine
WORKDIR /init
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY init.mjs secrets-check.mjs admin-check.mjs ./
USER node
CMD ["node", "/init/init.mjs"]
