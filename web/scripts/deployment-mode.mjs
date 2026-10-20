#!/usr/bin/env node
// 公開構成かどうかを終了コードで答える。docker-entrypoint.sh が、seed-admin.mjs の異常終了の扱いを決めるために使う。
//
//   終了コード 10 : ローカル構成と確認できた
//   終了コード  0 : 公開構成
//   それ以外      : 判定できなかった（クラッシュなど）。呼び出し側は公開構成として扱う（fail-closed）
//
// 「ローカル」を 10 という目立つ値にしているのは、node の異常終了（既定の 1 や、シグナルの 128 以上）が
// 偶然「ローカル」に読まれないようにするため。
import { webPublicDeploymentReasons } from './deployment-check.mjs';

const reasons = webPublicDeploymentReasons(process.env);
if (reasons.length > 0) {
  console.log(`public: ${reasons.join(', ')}`);
  process.exit(0);
}
console.log('local');
process.exit(10);
