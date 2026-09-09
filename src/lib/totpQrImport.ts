import jsQR from "jsqr";
import { Root } from "protobufjs/light.js";
import type { TOTPInput } from "./twoFactor";

export interface MigrationPart { batchId: number; batchIndex: number; batchSize: number; credentials: TOTPInput[] }
export interface MigrationBatch { batchId: number; batchSize: number; parts: Array<[number, TOTPInput[]]> }
interface OTPParameter { type?: number; secret?: Uint8Array; issuer?: string; name?: string; algorithm?: number; digits?: number }
interface MigrationPayload { otpParameters?: OTPParameter[]; version?: number; batchId?: number; batchIndex?: number; batchSize?: number }

const migrationPayloadType = Root.fromJSON({
  nested: {
    MigrationPayload: { fields: {
      otpParameters: { rule: "repeated", type: "OtpParameters", id: 1 },
      version: { type: "int32", id: 2 }, batchSize: { type: "int32", id: 3 },
      batchIndex: { type: "int32", id: 4 }, batchId: { type: "int32", id: 5 }
    }},
    OtpParameters: { fields: {
      secret: { type: "bytes", id: 1 }, name: { type: "string", id: 2 },
      issuer: { type: "string", id: 3 }, algorithm: { type: "int32", id: 4 },
      digits: { type: "int32", id: 5 }, type: { type: "int32", id: 6 }, counter: { type: "int64", id: 7 }
    }}
  }
}).lookupType("MigrationPayload");

function decodeBase64Url(value: string): Uint8Array | null {
  try {
    const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
    const binary = atob(normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "="));
    return Uint8Array.from(binary, (character) => character.charCodeAt(0));
  } catch { return null; }
}

function encodeBase32(bytes: Uint8Array): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let buffer = 0; let bits = 0; let result = "";
  for (const byte of bytes) {
    buffer = (buffer << 8) | byte; bits += 8;
    while (bits >= 5) { result += alphabet[(buffer >>> (bits - 5)) & 31]; bits -= 5; }
  }
  if (bits > 0) result += alphabet[(buffer << (5 - bits)) & 31];
  return result;
}

function supportedBase32(value: string): string {
  const normalized = value.trim().replace(/[\s-]/g, "").toUpperCase();
  return /^[A-Z2-7]+=*$/.test(normalized) ? normalized : "";
}

function migrationCredential(item: OTPParameter): TOTPInput | null {
  if (!item || Number(item.type) !== 2 || !item.secret?.length) return null;
  if (![0, 1, 2, 3].includes(item.algorithm || 0) || ![0, 1, 2].includes(item.digits || 0)) return null;
  const secret = supportedBase32(encodeBase32(item.secret));
  if (!secret) return null;
  const issuer = String(item.issuer || "").trim();
  const account = String(item.name || "").trim();
  return {
    secret, issuer, account,
    label: [issuer, account].filter(Boolean).join(issuer && account ? ":" : "") || "Google Authenticator",
    algorithm: Number(item.algorithm) === 2 ? "SHA256" : Number(item.algorithm) === 3 ? "SHA512" : "SHA1",
    digits: Number(item.digits) === 2 ? 8 : 6, period: 30
  };
}

export type TOTPQrPayload =
  | { kind: "uri"; uri: string }
  | { kind: "migration"; batch: MigrationPart };

export function parseTOTPQrPayload(raw: string): TOTPQrPayload | null {
  const value = String(raw || "").trim();
  if (value.length > 128 * 1024) return null;
  if (value.startsWith("otpauth://")) {
    try {
      const uri = new URL(value);
      if (uri.protocol !== "otpauth:" || uri.hostname.toLowerCase() !== "totp") return null;
      return { kind: "uri", uri: value };
    } catch { return null; }
  }
  if (!value.startsWith("otpauth-migration://")) return null;
  try {
    const uri = new URL(value);
    if (uri.protocol !== "otpauth-migration:" || uri.hostname !== "offline") return null;
    const bytes = decodeBase64Url(uri.searchParams.get("data") || "");
    if (!bytes) return null;
    const decoded = migrationPayloadType.decode(bytes) as MigrationPayload;
    if ((decoded.version || 1) !== 1) return null;
    const batchSize = Math.max(1, Number(decoded.batchSize) || 1);
    const batchIndex = Number(decoded.batchIndex) || 0;
    if (batchSize > 100 || batchIndex < 0 || batchIndex >= batchSize) return null;
    const credentials = (decoded.otpParameters || []).map(migrationCredential).filter((item): item is TOTPInput => item !== null);
    if (!credentials.length) return null;
    return { kind: "migration", batch: {
      batchId: Number(decoded.batchId) || 0, batchIndex, batchSize, credentials
    }};
  } catch { return null; }
}

export function mergeTOTPQrMigrationBatch(current: MigrationBatch | null, next: MigrationPart): MigrationBatch {
  if (current && (current.batchId !== next.batchId || current.batchSize !== next.batchSize)) throw new Error("迁移批次不一致，请先完成当前批次。");
  const parts = new Map<number, TOTPInput[]>(current?.parts || []);
  parts.set(next.batchIndex, next.credentials);
  return { batchId: next.batchId, batchSize: Math.max(current?.batchSize || 0, next.batchSize || 1), parts: Array.from(parts.entries()).sort(([a], [b]) => a - b) };
}

export function migrationBatchCredentials(batch: MigrationBatch | null): TOTPInput[] {
  const unique = new Map<string, TOTPInput>();
  for (const [, credentials] of batch?.parts || []) for (const credential of credentials || []) {
    const key = [credential.secret, credential.algorithm, credential.digits, credential.period].join(":");
    if (credential.secret && !unique.has(key)) unique.set(key, credential);
  }
  return Array.from(unique.values());
}

export async function decodeTOTPQrImage(blob: Blob): Promise<string | null> {
  if (blob.size > 10 * 1024 * 1024) throw new Error("二维码图片超过 10 MB，请使用较小的截图。");
  const imageUrl = URL.createObjectURL(blob);
  try {
    const image = await new Promise<HTMLImageElement>((resolve, reject) => {
      const element = new Image(); element.onload = () => resolve(element); element.onerror = reject; element.src = imageUrl;
    });
    const scale = Math.min(1, 2200 / Math.max(image.naturalWidth, image.naturalHeight));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(image.naturalWidth * scale)); canvas.height = Math.max(1, Math.round(image.naturalHeight * scale));
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context) return null;
    context.drawImage(image, 0, 0, canvas.width, canvas.height);
    const source = context.getImageData(0, 0, canvas.width, canvas.height);
    return jsQR(source.data, source.width, source.height, { inversionAttempts: "attemptBoth" })?.data?.trim() || null;
  } finally { URL.revokeObjectURL(imageUrl); }
}
