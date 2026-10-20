import nodemailer from 'nodemailer';

export function createTransporter() {
  const port = Number(process.env.SMTP_PORT) || 587;
  const user = (process.env.SMTP_USER || '').trim();
  return nodemailer.createTransport({
    host: process.env.SMTP_HOST,
    port,
    // 465 は暗黙 TLS、それ以外は STARTTLS（サーバーが対応していれば昇格）
    secure: port === 465,
    ...(user ? { auth: { user, pass: process.env.SMTP_PASS } } : {}),
  });
}
