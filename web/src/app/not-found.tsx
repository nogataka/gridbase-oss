// app/not-found.tsx
import Link from 'next/link';

export default function NotFound() {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        minHeight: '100vh',
        gap: 16,
        padding: 16,
        textAlign: 'center',
      }}
    >
      <h1 style={{ fontSize: '2rem', margin: 0 }}>404 - ページが見つかりません</h1>
      <p style={{ color: '#666', margin: 0, lineHeight: 1.6 }}>
        お探しのページは存在しないか、移動した可能性があります。
      </p>
      <Link href="/">ホームに戻る</Link>
    </div>
  );
}
