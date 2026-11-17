export type UploadInput = {
  file: string;
  title: string;
  description: string;
  caption: string;
  tags: string[];
};

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** fetch yang melempar error berisi isi respons jika status bukan 2xx. */
export async function fetchJson<T = any>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  const text = await res.text();
  let body: any;
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    body = { raw: text };
  }
  if (!res.ok) throw new Error(`HTTP ${res.status} dari ${new URL(url).host}: ${text.slice(0, 500)}`);
  return body as T;
}
