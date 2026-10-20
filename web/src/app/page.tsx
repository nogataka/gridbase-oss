import { redirect } from 'next/navigation';
import { getServerSession } from 'next-auth';
import { authOptions } from '@/lib/auth';

export const dynamic = 'force-dynamic';

// LP は持たない。ログイン済みならダッシュボード、未ログインならサインインへ。
export default async function Home() {
  const session = await getServerSession(authOptions);
  redirect(session?.user?.uid ? '/dashboard' : '/signin');
}
