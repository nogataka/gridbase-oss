#!/bin/sh
# 起動時に初回管理者を（必要なら）作成してから Next.js を起動する。
#
# seed-admin.mjs の終了コード:
#   0 : 作成した / 不要だった。web を起動する
#   3 : 公開構成で既定のパスワードを検出、または公開構成で検査・作成に失敗。安全のため web を起動しない
#   それ以外（1: ローカル構成の失敗、Node の未処理の例外、OOM などのシグナル 137 ...）:
#       scripts/deployment-mode.mjs で構成を調べ、ローカル構成と確認できたときだけ、ログに残して web の起動を続ける。
#       公開構成、または判定できないときは起動しない（公開構成では「検査できない」も起動しない側に倒す）
#
# 再実行は  docker compose run --rm web node scripts/seed-admin.mjs
node scripts/seed-admin.mjs
status=$?
if [ "$status" -eq 3 ]; then
  echo "[entrypoint] 公開構成で既定のパスワードを検出した、または安全を確認できなかったため、web を起動しません。上のエラーを確認してください" >&2
  exit 1
elif [ "$status" -ne 0 ]; then
  node scripts/deployment-mode.mjs >/dev/null 2>&1
  mode=$?
  if [ "$mode" -ne 10 ]; then
    echo "[entrypoint] seed-admin が異常終了しました（終了コード $status）。公開構成、または構成を判定できないため、web を起動しません。上のログを確認してください" >&2
    exit 1
  fi
  echo "[entrypoint] seed-admin に失敗しました（終了コード $status）。ローカル構成なので、web は起動を続けます。ログを確認してください"
fi
exec node server.js
